package monitor

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/shopspring/decimal"
	"github.com/spf13/cast"
	"github.com/Psticso/bepusdt/app/config"
	"github.com/Psticso/bepusdt/app/help"
	"github.com/Psticso/bepusdt/app/log"
	"github.com/Psticso/bepusdt/app/model"
	"github.com/Psticso/bepusdt/app/notify"
	"github.com/Psticso/bepusdt/app/telegram"
	"github.com/Psticso/tronprotocol/api"
	"github.com/Psticso/tronprotocol/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"strconv"
	"time"
)

// 交易所在区块高度和当前区块高度差值超过20，说明此交易已经被网络确认
const blockHeightNumConfirmedSub = 20

// usdt trc20 contract address 41a614f803b6fd780986a42c78ec9c7f77e6ded13c TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t
var usdtTrc20ContractAddress = []byte{0x41, 0xa6, 0x14, 0xf8, 0x03, 0xb6, 0xfd, 0x78, 0x09, 0x86, 0xa4, 0x2c, 0x78, 0xec, 0x9c, 0x7f, 0x77, 0xe6, 0xde, 0xd1, 0x3c}

var currentBlockHeight int64

type resource struct {
	ID           string
	Type         core.Transaction_Contract_ContractType
	Balance      int64
	FromAddress  string
	RecvAddress  string
	Timestamp    time.Time
	ResourceCode core.ResourceCode
}

type transfer struct {
	ID          string
	Amount      float64
	FromAddress string
	RecvAddress string
	Timestamp   time.Time
	TradeType   string
}

type usdtTrc20TransferRaw struct {
	RecvAddress string
	Amount      float64
}

func init() {
	RegisterSchedule(time.Second*3, BlockScanStart)
}

// BlockScanStart 区块扫描
func BlockScanStart(duration time.Duration) {
	var node = config.GetTronGrpcNode()
	log.Info("区块扫描启动：", node)

	// 设置重连参数
	reParams := grpc.ConnectParams{
		Backoff: backoff.Config{
			BaseDelay:  1 * time.Second,  // 初始重连延迟
			MaxDelay:   30 * time.Second, // 最大重连延迟
			Multiplier: 1.5,              // 延迟增长乘数
		},
		MinConnectTimeout: 1 * time.Minute, // 最小连接超时时间
	}

	conn, err := grpc.NewClient(node, grpc.WithConnectParams(reParams), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {

		log.Error("grpc.NewClient", err)
	}

	defer conn.Close()

	var client = api.NewWalletClient(conn)

	for range time.Tick(duration) { // 3秒产生一个区块
		var ctx1, cancel1 = context.WithTimeout(context.Background(), time.Second*3)
		nowBlock, err1 := client.GetNowBlock2(ctx1, nil) // 获取当前区块高度
		cancel1()
		if err1 != nil {
			log.Warn("GetNowBlock 超时：", err1)

			continue
		}

		if currentBlockHeight == 0 { // 初始化当前区块高度

			currentBlockHeight = nowBlock.BlockHeader.RawData.Number - 1
		}

		// 连续区块
		var sub = nowBlock.BlockHeader.RawData.Number - currentBlockHeight
		if sub == 1 {
			parseBlockTrans(nowBlock, nowBlock.BlockHeader.RawData.Number)

			continue
		}

		// 如果当前区块高度和上次扫描的区块高度差值超过1，说明存在区块丢失
		var startBlockHeight = currentBlockHeight + 1
		var endBlockHeight = nowBlock.BlockHeader.RawData.Number

		// 扫描丢失的区块
		var ctx2, cancel2 = context.WithTimeout(context.Background(), time.Second*3)
		blocks, err2 := client.GetBlockByLimitNext2(ctx2, &api.BlockLimit{StartNum: startBlockHeight, EndNum: endBlockHeight})
		cancel2()
		if err2 != nil {
			log.Warn("GetBlockByLimitNext2 超时：", err2)

			continue
		}

		// 扫描丢失区块
		for _, block := range blocks.GetBlock() {
			parseBlockTrans(block, block.BlockHeader.RawData.Number)
		}
	}
}

// parseBlockTrans 解析区块交易
func parseBlockTrans(block *api.BlockExtention, nowHeight int64) {
	currentBlockHeight = nowHeight

	var resources = make([]resource, 0)
	var transfers = make([]transfer, 0)
	var timestamp = time.UnixMilli(block.GetBlockHeader().GetRawData().GetTimestamp())
	for _, v := range block.GetTransactions() {
		if !v.Result.Result {

			continue
		}

		var itm = v.GetTransaction()
		var id = hex.EncodeToString(v.Txid)
		for _, contract := range itm.GetRawData().GetContract() {
			// 资源代理 DelegateResourceContract
			if contract.GetType() == core.Transaction_Contract_DelegateResourceContract {
				var foo = &core.DelegateResourceContract{}
				err := contract.GetParameter().UnmarshalTo(foo)
				if err != nil {

					continue
				}

				resources = append(resources, resource{
					ID:           id,
					Type:         core.Transaction_Contract_DelegateResourceContract,
					Balance:      foo.Balance,
					ResourceCode: foo.Resource,
					FromAddress:  base58CheckEncode(foo.OwnerAddress),
					RecvAddress:  base58CheckEncode(foo.ReceiverAddress),
					Timestamp:    timestamp,
				})
			}

			// 资源回收 UnDelegateResourceContract
			if contract.GetType() == core.Transaction_Contract_UnDelegateResourceContract {
				var foo = &core.UnDelegateResourceContract{}
				err := contract.GetParameter().UnmarshalTo(foo)
				if err != nil {

					continue
				}

				resources = append(resources, resource{
					ID:           id,
					Type:         core.Transaction_Contract_UnDelegateResourceContract,
					Balance:      foo.Balance,
					ResourceCode: foo.Resource,
					FromAddress:  base58CheckEncode(foo.OwnerAddress),
					RecvAddress:  base58CheckEncode(foo.ReceiverAddress),
					Timestamp:    timestamp,
				})
			}

			// TRX转账交易
			if contract.GetType() == core.Transaction_Contract_TransferContract {
				var foo = &core.TransferContract{}
				err := contract.GetParameter().UnmarshalTo(foo)
				if err != nil {

					continue
				}

				transfers = append(transfers, transfer{
					ID:          id,
					Amount:      float64(foo.Amount),
					FromAddress: base58CheckEncode(foo.OwnerAddress),
					RecvAddress: base58CheckEncode(foo.ToAddress),
					Timestamp:   timestamp,
					TradeType:   model.OrderTradeTypeTronTrx,
				})

				continue
			}

			// 触发智能合约
			if contract.GetType() == core.Transaction_Contract_TriggerSmartContract {
				var foo = &core.TriggerSmartContract{}
				err := contract.GetParameter().UnmarshalTo(foo)
				if err != nil {

					continue
				}

				var transItem = transfer{Timestamp: timestamp, ID: id, FromAddress: base58CheckEncode(foo.OwnerAddress)}
				var reader = bytes.NewReader(foo.GetData())
				if !bytes.Equal(foo.GetContractAddress(), usdtTrc20ContractAddress) { // usdt trc20 contract

					continue
				}

				// 解析合约数据
				var trc20Contract = parseUsdtTrc20Contract(reader)
				if trc20Contract.Amount == 0 {

					continue
				}

				transItem.TradeType = model.OrderTradeTypeUsdtTrc20
				transItem.Amount = trc20Contract.Amount
				transItem.RecvAddress = trc20Contract.RecvAddress

				transfers = append(transfers, transItem)
			}
		}
	}

	if len(transfers) > 0 {
		handleOtherNotify(handleOrderTransaction(block.GetBlockHeader().GetRawData().GetNumber(), nowHeight, transfers))
	}

	if len(resources) > 0 {
		handleResourceNotify(resources)
	}

	log.Info("区块扫描完成：", nowHeight)
}

// parseUsdtTrc20Contract 解析usdt trc20合约
func parseUsdtTrc20Contract(reader *bytes.Reader) usdtTrc20TransferRaw {
	var funcName = make([]byte, 4)
	_, err = reader.Read(funcName)
	if err != nil {
		// 读取funcName失败

		return usdtTrc20TransferRaw{}
	}
	if !bytes.Equal(funcName, []byte{0xa9, 0x05, 0x9c, 0xbb}) { // a9059cbb transfer(address,uint256)
		// funcName不匹配transfer

		return usdtTrc20TransferRaw{}
	}

	var addressBytes = make([]byte, 20)
	_, err = reader.ReadAt(addressBytes, 4+12)
	if err != nil {
		// 读取toAddress失败

		return usdtTrc20TransferRaw{}
	}

	var toAddress = base58CheckEncode(append([]byte{0x41}, addressBytes...))
	var value = make([]byte, 32)
	_, err = reader.ReadAt(value, 36)
	if err != nil {
		// 读取value失败

		return usdtTrc20TransferRaw{}
	}

	var amount, _ = strconv.ParseInt(hex.EncodeToString(value), 16, 64)

	return usdtTrc20TransferRaw{RecvAddress: toAddress, Amount: float64(amount)}
}

// handleOrderTransaction 处理支付交易
func handleOrderTransaction(refBlockNum, nowHeight int64, transfers []transfer) []transfer {
	var orders, err = getAllPendingOrders()
	var notOrderTransfers []transfer
	if err != nil {
		log.Error(err.Error())

		return notOrderTransfers
	}

	for _, t := range transfers {
		// 计算交易金额
		var amount, quant = parseTransAmount(t.Amount)

		// 判断金额是否在允许范围内
		if !inPaymentAmountRange(amount) {

			continue
		}

		// 判断是否存在对应订单
		order, isOrder := orders[fmt.Sprintf("%s%v%s", t.RecvAddress, quant, t.TradeType)]
		if !isOrder {
			notOrderTransfers = append(notOrderTransfers, t)

			continue
		}

		// 判断时间是否在有效期内
		if t.Timestamp.Unix() < order.CreatedAt.Unix() || t.Timestamp.Unix() > order.ExpiredAt.Unix() {
			// 已失效

			continue
		}

		// 更新订单交易信息
		var err = order.OrderUpdateTxInfo(refBlockNum, t.FromAddress, t.ID, t.Timestamp)
		if err != nil {

			log.Error("OrderUpdateTxInfo", err)
		}
	}

	for _, order := range orders {
		if order.RefBlockNum == 0 || order.TradeHash == "" {

			continue
		}

		// 判断交易是否需要被确认
		var confirmedSub int64 = 0
		if config.GetTradeConfirmed() {

			confirmedSub = blockHeightNumConfirmedSub
		}

		if nowHeight-order.RefBlockNum <= confirmedSub {

			continue
		}

		var err = order.OrderSetSucc()
		if err != nil {
			log.Error("OrderSetSucc", err)

			continue
		}

		go notify.Handle(order)             // 通知订单支付成功
		go telegram.SendTradeSuccMsg(order) // TG发送订单信息
	}

	return notOrderTransfers
}

// handleOtherNotify 处理其他通知
func handleOtherNotify(items []transfer) {
	var ads []model.WalletAddress
	var tx = model.DB.Where("status = ? and other_notify = ?", model.StatusEnable, model.OtherNotifyEnable).Find(&ads)
	if tx.RowsAffected <= 0 {

		return
	}

	for _, wa := range ads {
		for _, trans := range items {
			if trans.RecvAddress != wa.Address && trans.FromAddress != wa.Address {

				continue
			}

			var _, amount = parseTransAmount(trans.Amount)
			var detailUrl = "https://tronscan.org/#/transaction/" + trans.ID
			if !model.IsNeedNotifyByTxid(trans.ID) {
				// 不需要额外通知

				continue
			}

			var title = "收入"
			if trans.RecvAddress != wa.Address {
				title = "支出"
			}

			var transferUnit = "USDT.TRC20"
			var transferType = "USDT"
			if trans.TradeType == model.OrderTradeTypeTronTrx {
				transferUnit = "TRX"
				transferType = "TRX"
			}

			{
				// 忽视小额非订单交易监控通知，暂时写死，等待后续优化
				if trans.TradeType == model.OrderTradeTypeTronTrx && cast.ToFloat64(amount) < 0.01 {

					continue
				}
				if trans.TradeType == model.OrderTradeTypeUsdtTrc20 && cast.ToFloat64(amount) < 0.0001 {

					continue
				}
			}

			var text = fmt.Sprintf(
				"#账户%s #非订单交易 #"+transferType+"\n---\n```\n💲交易数额：%v "+transferUnit+"\n⏱️交易时间：%v\n✅接收地址：%v\n🅾️发送地址：%v```\n",
				title,
				amount,
				trans.Timestamp.Format(time.DateTime),
				help.MaskAddress(trans.RecvAddress),
				help.MaskAddress(trans.FromAddress),
			)

			var chatId, err = strconv.ParseInt(config.GetTgBotNotifyTarget(), 10, 64)
			if err != nil {

				continue
			}

			var msg = tgbotapi.NewMessage(chatId, text)
			msg.ParseMode = tgbotapi.ModeMarkdown
			msg.ReplyMarkup = tgbotapi.InlineKeyboardMarkup{
				InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{
					{
						tgbotapi.NewInlineKeyboardButtonURL("📝查看交易明细", detailUrl),
					},
				},
			}

			var _record = model.NotifyRecord{Txid: trans.ID}
			model.DB.Create(&_record)

			go telegram.SendMsg(msg)
		}
	}
}

// handleResourceNotify 处理资源通知
func handleResourceNotify(items []resource) {
	var ads []model.WalletAddress
	var tx = model.DB.Where("status = ? and other_notify = ?", model.StatusEnable, model.OtherNotifyEnable).Find(&ads)
	if tx.RowsAffected <= 0 {

		return
	}

	for _, wa := range ads {
		for _, trans := range items {
			if trans.RecvAddress != wa.Address && trans.FromAddress != wa.Address {

				continue
			}

			if trans.ResourceCode != core.ResourceCode_ENERGY {

				continue
			}

			var detailUrl = "https://tronscan.org/#/transaction/" + trans.ID
			if !model.IsNeedNotifyByTxid(trans.ID) {
				// 不需要额外通知

				continue
			}

			var title = "代理"
			if trans.Type == core.Transaction_Contract_UnDelegateResourceContract {
				title = "回收"
			}

			var text = fmt.Sprintf(
				"#资源动态 #能量"+title+"\n---\n```\n🔋质押数量："+cast.ToString(trans.Balance/1000000)+"\n⏱️交易时间：%v\n✅操作地址：%v\n🅾️资源来源：%v```\n",
				trans.Timestamp.Format(time.DateTime),
				help.MaskAddress(trans.RecvAddress),
				help.MaskAddress(trans.FromAddress),
			)

			var chatId, err = strconv.ParseInt(config.GetTgBotNotifyTarget(), 10, 64)
			if err != nil {

				continue
			}

			var msg = tgbotapi.NewMessage(chatId, text)
			msg.ParseMode = tgbotapi.ModeMarkdown
			msg.ReplyMarkup = tgbotapi.InlineKeyboardMarkup{
				InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{
					{
						tgbotapi.NewInlineKeyboardButtonURL("📝查看交易明细", detailUrl),
					},
				},
			}

			var _record = model.NotifyRecord{Txid: trans.ID}
			model.DB.Create(&_record)

			go telegram.SendMsg(msg)
		}
	}
}

func base58CheckEncode(input []byte) string {
	checksum := chainhash.DoubleHashB(input)
	checksum = checksum[:4]

	input = append(input, checksum...)

	return base58.Encode(input)
}

// 列出所有等待支付的交易订单
func getAllPendingOrders() (map[string]model.TradeOrders, error) {
	tradeOrders, err := model.GetTradeOrderByStatus(model.OrderStatusWaiting)
	if err != nil {

		return nil, fmt.Errorf("待支付订单获取失败: %w", err)
	}

	var lock = make(map[string]model.TradeOrders) // 当前所有正在等待支付的订单 Lock Key
	for _, order := range tradeOrders {
		if time.Now().Unix() >= order.ExpiredAt.Unix() { // 订单过期
			err := order.OrderSetExpired()
			if err != nil {
				log.Error("订单过期标记失败：", err, order.OrderId)
			} else {
				log.Info("订单过期：", order.OrderId)
			}

			continue
		}

		lock[order.Address+order.Amount+order.TradeType] = order
	}

	return lock, nil
}

// 解析交易金额
func parseTransAmount(amount float64) (decimal.Decimal, string) {
	var _decimalAmount = decimal.NewFromFloat(amount)
	var _decimalDivisor = decimal.NewFromFloat(1000000)
	var result = _decimalAmount.Div(_decimalDivisor)

	return result, result.String()
}

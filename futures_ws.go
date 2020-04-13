package okex

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/recws-org/recws"
	"github.com/tidwall/gjson"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	TableFuturesTicker     = "futures/ticker"       // 公共-Ticker频道
	TableFuturesTrade      = "futures/trade"        // 公共-交易频道
	TableFuturesDepthL2Tbt = "futures/depth_l2_tbt" // 公共-400档增量数据频道
	TableFuturesPosition   = "futures/position"     // 用户持仓频道
	TableFuturesAccount    = "futures/account"      // 用户账户频道
	TableFuturesOrder      = "futures/order"        // 用户交易频道

	ActionDepthL2Partial = "partial"
	ActionDepthL2Update  = "update"
)

type FuturesWS struct {
	sync.RWMutex

	wsURL      string
	accessKey  string
	secretKey  string
	passphrase string

	ctx    context.Context
	cancel context.CancelFunc
	wsConn recws.RecConn

	subscriptions map[string]interface{}

	tickersCallback    func(tickers []WSTicker)
	tradesCallback     func(trades []WSTrade)
	depthL2TbtCallback func(action string, data []WSDepthL2Tbt)
	accountCallback    func(accounts []WSAccount)
	positionCallback   func(positions []WSFuturesPosition)
	orderCallback      func(orders []WSOrder)
}

// SetProxy 设置代理地址
// porxyURL:
// socks5://127.0.0.1:1080
// https://127.0.0.1:1080
func (ws *FuturesWS) SetProxy(proxyURL string) (err error) {
	var purl *url.URL
	purl, err = url.Parse(proxyURL)
	if err != nil {
		return
	}
	log.Printf("[ws][%s] proxy url:%s", proxyURL, purl)
	ws.wsConn.Proxy = http.ProxyURL(purl)
	return
}

func (ws *FuturesWS) SetTickerCallback(callback func(tickers []WSTicker)) {
	ws.tickersCallback = callback
}

func (ws *FuturesWS) SetTradeCallback(callback func(trades []WSTrade)) {
	ws.tradesCallback = callback
}

func (ws *FuturesWS) SetDepthL2TbtCallback(callback func(action string, data []WSDepthL2Tbt)) {
	ws.depthL2TbtCallback = callback
}

func (ws *FuturesWS) SetAccountCallback(callback func(accounts []WSAccount)) {
	ws.accountCallback = callback
}

func (ws *FuturesWS) SetPositionCallback(callback func(positions []WSFuturesPosition)) {
	ws.positionCallback = callback
}

func (ws *FuturesWS) SetOrderCallback(callback func(orders []WSOrder)) {
	ws.orderCallback = callback
}

func (ws *FuturesWS) SubscribeTicker(id string, symbol string) error {
	ch := fmt.Sprintf("%v:%v", TableFuturesTicker, symbol)
	return ws.Subscribe(id, []string{ch})
}

func (ws *FuturesWS) SubscribeTrade(id string, symbol string) error {
	ch := fmt.Sprintf("%v:%v", TableFuturesTrade, symbol)
	return ws.Subscribe(id, []string{ch})
}

// SubscribeDepthL2Tbt 公共-400档增量数据频道
// 订阅后首次返回市场订单簿的400档深度数据并推送；后续只要订单簿深度有变化就推送有更改的数据。
func (ws *FuturesWS) SubscribeDepthL2Tbt(id string, symbol string) error {
	ch := fmt.Sprintf("%v:%v", TableFuturesDepthL2Tbt, symbol)
	return ws.Subscribe(id, []string{ch})
}

func (ws *FuturesWS) SubscribePosition(id string, symbol string) error {
	ch := fmt.Sprintf("%v:%v", TableFuturesPosition, symbol)
	return ws.Subscribe(id, []string{ch})
}

func (ws *FuturesWS) SubscribeAccount(id string, symbol string) error {
	ch := fmt.Sprintf("%v:%v", TableFuturesAccount, symbol)
	return ws.Subscribe(id, []string{ch})
}

func (ws *FuturesWS) SubscribeOrder(id string, symbol string) error {
	ch := fmt.Sprintf("%v:%v", TableFuturesOrder, symbol)
	return ws.Subscribe(id, []string{ch})
}

// Subscribe 订阅
func (ws *FuturesWS) Subscribe(id string, args []string) error {
	ws.Lock()
	defer ws.Unlock()

	type Op struct {
		Op   string   `json:"op"`
		Args []string `json:"args"`
	}

	op := Op{
		Op:   "subscribe",
		Args: args,
	}
	ws.subscriptions[id] = op
	return ws.sendWSMessage(op)
}

// Unsubscribe 取消订阅
func (ws *FuturesWS) Unsubscribe(id string) error {
	ws.Lock()
	defer ws.Unlock()

	if _, ok := ws.subscriptions[id]; ok {
		delete(ws.subscriptions, id)
	}
	return nil
}

func (ws *FuturesWS) Login() error {
	if ws.accessKey == "" || ws.secretKey == "" || ws.passphrase == "" {
		return fmt.Errorf("missing key")
	}
	timestamp := EpochTime()

	preHash := PreHashString(timestamp, GET, "/users/self/verify", "")
	if sign, err := HmacSha256Base64Signer(preHash, ws.secretKey); err != nil {
		return err
	} else {
		op, err := loginOp(ws.accessKey, ws.passphrase, timestamp, sign)
		if err != nil {
			return err
		}
		//data, err := Struct2JsonString(op)
		log.Printf("Send Msg: %#v", *op)
		//err = a.conn.WriteMessage(websocket.TextMessage, []byte(data))
		err = ws.sendWSMessage(op)
		if err != nil {
			return err
		}
		time.Sleep(time.Millisecond * 100)
	}
	return nil
}

func (ws *FuturesWS) subscribeHandler() error {
	//log.Printf("subscribeHandler")
	ws.Lock()
	defer ws.Unlock()

	err := ws.Login()
	if err != nil {
		log.Printf("login error: %v", err)
	}

	for _, v := range ws.subscriptions {
		//log.Printf("sub: %#v", v)
		err := ws.sendWSMessage(v)
		if err != nil {
			log.Printf("%v", err)
		}
	}
	return nil
}

func (ws *FuturesWS) sendWSMessage(msg interface{}) error {
	return ws.wsConn.WriteJSON(msg)
}

func (ws *FuturesWS) Start() {
	log.Printf("wsURL: %v", ws.wsURL)
	ws.wsConn.Dial(ws.wsURL, nil)
	go ws.run()
}

func (ws *FuturesWS) run() {
	ctx := context.Background()
	for {
		select {
		case <-ctx.Done():
			go ws.wsConn.Close()
			log.Printf("Websocket closed %s", ws.wsConn.GetURL())
			return
		default:
			messageType, msg, err := ws.wsConn.ReadMessage()
			if err != nil {
				log.Printf("Read error: %v", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			msg, err = FlateUnCompress(msg)
			if err != nil {
				log.Printf("%v", err)
				continue
			}

			ws.handleMsg(messageType, msg)
		}
	}
}

func (ws *FuturesWS) handleMsg(messageType int, msg []byte) {
	ret := gjson.ParseBytes(msg)
	// 登录成功
	// {"event":"login","success":true}

	// Ticker 订阅成功
	// {"event":"subscribe","channel":"futures/ticker:BTC-USD-200626"}

	// 错误消息
	// {"event":"error","message":"User not logged in / User must be logged in","errorCode":30041}

	// 委托
	// {"table":"futures/order","data":[{"leverage":"10","last_fill_time":"1970-01-01T00:00:00.000Z","filled_qty":"0","fee":"0","price_avg":"0","type":"1","client_oid":"","last_fill_qty":"0","instrument_id":"BTC-USD-200626","last_fill_px":"0","pnl":"0","size":"1","price":"6200","last_fill_id":"0","error_code":"0","state":"0","contract_val":"100","order_id":"4718613348289537","order_type":"0","timestamp":"2020-04-13T00:05:25.760Z","status":"0"}]}

	// 账户资产变动
	// {"table":"futures/account","data":[{"BTC":{"available":"0.01867179","can_withdraw":"0.01867179","currency":"BTC","equity":"0.02018113","liqui_mode":"tier","maint_margin_ratio":"0.005","margin":"0.00150934","margin_for_unfilled":"0","margin_frozen":"0.00150934","margin_mode":"crossed","margin_ratio":"1.3370831","open_max":"0","realized_pnl":"-0.00034029","timestamp":"2020-04-12T08:00:13.975Z","total_avail_balance":"0.02046331","underlying":"BTC-USD","unrealized_pnl":"0.00005811"}}]}

	// Ticker 消息
	// {"table":"futures/ticker","data":[{"last":"6768.28","open_24h":"6887.16","best_bid":"6765.49","high_24h":"6889.64","low_24h":"6711","volume_24h":"4012676","volume_token_24h":"59026.9171","best_ask":"6765.5","open_interest":"2789329","instrument_id":"BTC-USD-200626","timestamp":"2020-04-12T06:19:03.829Z","best_bid_size":"129","best_ask_size":"6","last_qty":"2"}]}
	if tableValue := ret.Get("table"); tableValue.Exists() {
		table := tableValue.String()
		if table == TableFuturesDepthL2Tbt { // 优先判断最高频数据
			// {"table":"futures/depth_l2_tbt","action":"partial","data":[{"instrument_id":"BTC-USD-200626","asks":[["6773.41","344","0","8"],["6773.8","13","0","10"],["6773.81","40","0","1"],["6773.84","26","0","4"],["6773.86","1","0","1"],["6773.89","1","0","1"],["6774","4","0","2"],["6774.1","1","0","1"],["6774.14","2","0","1"],["6774.35","1","0","1"],["6774.36","70","0","3"],["6774.41","21","0","1"],["6774.44","1","0","1"],["6774.5","1","0","1"],["6774.95","1","0","1"],["6774.96","21","0","2"],["6775.21","1","0","1"],["6775.22","30","0","1"],["6775.28","1","0","1"],["6775.29","3","0","1"],["6775.6","1","0","1"],["6775.64","20","0","1"],["6775.72","84","0","1"],["6775.75","296","0","1"],["6775.76","2","0","2"],["6775.84","5","0","1"],["6775.86","134","0","2"],["6775.9","20","0","1"],["6776","3","0","1"],["6776.06","99","0","1"],["6776.07","1","0","1"],["6776.16","13","0","1"],["6776.19","13","0","1"],["6776.22","6","0","1"],["6776.26","14","0","2"],["6776.29","15","0","2"],["6776.31","20","0","1"],["6776.32","14","0","2"],["6776.36","8","0","1"],["6776.38","5","0","5"],["6776.42","2","0","1"],["6776.44","3","0","1"],["6776.45","2","0","1"],["6776.46","20","0","1"],["6776.47","9","0","2"],["6776.5","11","0","1"],["6776.53","39","0","3"],["6776.57","20","0","2"],["6776.61","18","0","1"],["6776.62","11","0","2"],["6776.63","6","0","1"],["6776.67","3","0","1"],["6776.69","12","0","1"],["6776.76","2","0","2"],["6776.79","3","0","3"],["6776.83","1","0","1"],["6776.9","1","0","1"],["6776.91","1","0","1"],["6776.97","100","0","1"],["6776.98","13","0","1"],["6777","3","0","1"],["6777.14","6","0","1"],["6777.16","10","0","1"],["6777.2","16","0","8"],["6777.24","26","0","3"],["6777.33","1","0","1"],["6777.38","1","0","1"],["6777.41","4","0","2"],["6777.43","7","0","7"],["6777.45","5","0","1"],["6777.49","5","0","1"],["6777.5","1","0","1"],["6777.53","15","0","1"],["6777.55","14","0","1"],["6777.58","6","0","1"],["6777.59","2","0","1"],["6777.6","1","0","1"],["6777.61","2","0","1"],["6777.66","1","0","1"],["6777.69","17","0","1"],["6777.72","2","0","2"],["6777.77","1","0","1"],["6777.78","10","0","1"],["6777.84","2","0","1"],["6777.88","50","0","1"],["6777.89","2","0","1"],["6777.92","5","0","1"],["6777.93","12","0","2"],["6777.96","9","0","1"],["6777.97","1","0","1"],["6778","3","0","1"],["6778.05","2","0","1"],["6778.06","3","0","1"],["6778.15","201","0","2"],["6778.25","18","0","9"],["6778.29","18","0","2"],["6778.3","60","0","1"],["6778.33","3","0","1"],["6778.39","16","0","1"],["6778.46","1","0","1"],["6778.48","1","0","1"],["6778.52","1075","0","1"],["6778.53","12","0","1"],["6778.59","5","0","5"],["6778.68","1","0","1"],["6778.82","12","0","4"],["6778.94","1","0","1"],["6778.99","5","0","1"],["6779","3","0","1"],["6779.06","18","0","1"],["6779.2","2","0","2"],["6779.23","4","0","4"],["6779.24","6","0","1"],["6779.27","656","0","1"],["6779.35","21","0","1"],["6779.39","40","0","1"],["6779.41","5","0","1"],["6779.44","67","0","1"],["6779.49","50","0","1"],["6779.56","50","0","1"],["6779.58","1","0","1"],["6779.6","1","0","1"],["6779.63","1","0","1"],["6779.67","5","0","1"],["6779.68","1","0","1"],["6779.73","1","0","1"],["6779.8","1","0","1"],["6779.95","83","0","1"],["6779.96","1","0","1"],["6780","1","0","1"],["6780.03","3","0","1"],["6780.04","5","0","5"],["6780.11","6","0","6"],["6780.17","5","0","1"],["6780.29","50","0","1"],["6780.3","100","0","1"],["6780.34","1","0","1"],["6780.4","73","0","1"],["6780.42","317","0","1"],["6780.49","111","0","1"],["6780.61","13","0","1"],["6780.62","14","0","1"],["6780.63","1","0","1"],["6780.65","159","0","1"],["6780.66","50","0","1"],["6780.74","197","0","2"],["6780.8","60","0","1"],["6780.83","234","0","2"],["6780.96","82","0","3"],["6780.97","1","0","1"],["6780.98","94","0","1"],["6781","3","0","1"],["6781.01","78","0","1"],["6781.06","12","0","1"],["6781.08","50","0","1"],["6781.09","89","0","1"],["6781.1","76","0","1"],["6781.11","10","0","1"],["6781.2","1","0","1"],["6781.22","121","0","1"],["6781.24","9","0","1"],["6781.25","134","0","1"],["6781.28","1","0","1"],["6781.36","5","0","1"],["6781.4","105","0","1"],["6781.42","200","0","1"],["6781.44","40","0","1"],["6781.47","18","0","1"],["6781.48","20","0","1"],["6781.6","40","0","1"],["6781.63","11","0","1"],["6781.64","11","0","1"],["6781.71","50","0","1"],["6781.85","10","0","1"],["6781.9","10","0","1"],["6781.93","33","0","1"],["6781.97","50","0","1"],["6782","135","0","2"],["6782.17","127","0","1"],["6782.21","116","0","1"],["6782.24","10","0","1"],["6782.25","50","0","1"],["6782.27","20","0","1"],["6782.29","3","0","1"],["6782.3","14","0","1"],["6782.31","100","0","2"],["6782.34","138","0","1"],["6782.35","50","0","1"],["6782.37","11","0","1"],["6782.41","29","0","2"],["6782.42","4","0","1"],["6782.45","71","0","1"],["6782.46","170","0","1"],["6782.47","10","0","1"],["6782.65","51","0","2"],["6782.66","154","0","1"],["6782.67","2","0","2"],["6782.69","1","0","1"],["6782.71","50","0","1"],["6782.73","50","0","1"],["6782.76","51","0","1"],["6782.86","28","0","1"],["6782.89","30","0","1"],["6782.99","7","0","1"],["6783","4","0","2"],["6783.05","50","0","1"],["6783.06","148","0","1"],["6783.14","132","0","1"],["6783.22","143","0","1"],["6783.23","4","0","4"],["6783.27","1","0","1"],["6783.28","50","0","1"],["6783.31","20","0","2"],["6783.37","50","0","1"],["6783.42","30","0","1"],["6783.48","165","0","1"],["6783.53","32","0","1"],["6783.54","15","0","1"],["6783.56","1","0","1"],["6783.59","3","0","3"],["6783.61","1","0","1"],["6783.64","10","0","1"],["6783.66","1","0","1"],["6783.69","181","0","1"],["6783.72","50","0","1"],["6783.73","11","0","2"],["6783.87","200","0","1"],["6783.91","1","0","1"],["6783.94","1","0","1"],["6784","74","0","2"],["6784.01","1","0","1"],["6784.02","84","0","1"],["6784.1","27","0","1"],["6784.15","7","0","7"],["6784.19","27","0","1"],["6784.23","186","0","1"],["6784.26","14","0","1"],["6784.32","16","0","1"],["6784.36","10","0","1"],["6784.48","1","0","1"],["6784.5","26","0","2"],["6784.57","3","0","1"],["6784.59","176","0","1"],["6784.63","4","0","1"],["6784.67","15","0","1"],["6784.78","2","0","1"],["6784.8","40","0","1"],["6784.83","2","0","1"],["6784.86","68","0","2"],["6784.88","1","0","1"],["6784.9","2","0","1"],["6784.92","6","0","1"],["6784.93","1","0","1"],["6784.98","1","0","1"],["6785","23","0","2"],["6785.03","1","0","1"],["6785.04","34","0","1"],["6785.07","71","0","2"],["6785.08","100","0","2"],["6785.14","16","0","1"],["6785.52","16","0","1"],["6785.82","1322","0","1"],["6785.91","30","0","1"],["6785.98","56","0","1"],["6786","25","0","4"],["6786.1","5","0","1"],["6786.2","34","0","1"],["6786.38","1","0","1"],["6786.41","1","0","1"],["6786.57","9","0","1"],["6786.76","50","0","1"],["6786.78","50","0","1"],["6786.87","1","0","1"],["6786.95","373","0","1"],["6787","3","0","1"],["6787.05","1","0","1"],["6787.15","10","0","1"],["6787.18","1","0","1"],["6787.23","70","0","3"],["6787.36","1","0","1"],["6787.41","264","0","1"],["6787.5","70","0","1"],["6787.57","9","0","1"],["6787.6","15","0","1"],["6787.67","50","0","1"],["6788.01","3","0","1"],["6788.08","40","0","1"],["6788.12","44","0","1"],["6788.17","10","0","1"],["6788.19","1","0","1"],["6788.27","1","0","1"],["6788.35","200","0","1"],["6788.37","203","0","1"],["6788.45","50","0","1"],["6788.47","5","0","1"],["6788.51","2","0","1"],["6788.52","4","0","1"],["6788.57","1","0","1"],["6788.78","10","0","1"],["6788.85","3","0","1"],["6789","3","0","1"],["6789.04","34","0","1"],["6789.09","1","0","1"],["6789.13","50","0","1"],["6789.14","50","0","1"],["6789.47","20","0","1"],["6789.5","1","0","1"],["6789.65","27","0","1"],["6789.79","20","0","1"],["6789.83","1","0","1"],["6789.97","40","0","1"],["6790","3","0","1"],["6790.22","33","0","1"],["6790.41","1","0","1"],["6790.45","26","0","2"],["6790.51","32","0","2"],["6790.83","5","0","1"],["6791","3","0","1"],["6791.08","200","0","2"],["6791.13","1","0","1"],["6791.19","22","0","1"],["6791.35","45","0","1"],["6791.42","27","0","1"],["6791.54","34","0","1"],["6791.64","22","0","1"],["6791.81","1","0","1"],["6791.89","64","0","2"],["6792","4","0","2"],["6792.1","5","0","1"],["6792.2","1","0","1"],["6792.3","1","0","1"],["6792.43","33","0","1"],["6792.58","57","0","1"],["6792.79","33","0","1"],["6792.92","4","0","1"],["6792.95","22","0","1"],["6793","3","0","1"],["6793.09","15","0","1"],["6793.11","199","0","1"],["6793.17","1","0","1"],["6793.21","4","0","1"],["6793.23","15","0","1"],["6793.24","34","0","1"],["6793.31","50","0","2"],["6793.59","20","0","1"],["6793.73","2","0","2"],["6793.77","15","0","1"],["6793.9","1","0","1"],["6793.93","34","0","1"],["6794.03","150","0","1"],["6794.06","34","0","1"],["6794.14","20","0","1"],["6794.19","3","0","1"],["6794.36","24","0","1"],["6794.4","218","0","2"],["6794.49","33","0","1"],["6794.57","21","0","1"],["6794.72","22","0","1"],["6794.77","1","0","1"],["6794.82","19","0","1"],["6794.92","12","0","1"],["6794.97","43","0","1"],["6795","193","0","6"],["6795.21","1","0","1"],["6795.3","34","0","1"],["6795.44","2","0","2"],["6795.53","475","0","1"],["6795.59","5","0","1"],["6795.65","1","0","1"],["6796","1003","0","3"],["6796.01","20","0","1"],["6796.03","19","0","1"],["6796.08","100","0","1"],["6796.35","2","0","1"],["6796.42","240","0","1"],["6796.45","2","0","1"],["6796.51","13","0","1"],["6796.56","14","0","1"],["6796.74","1","0","1"],["6796.84","101","0","1"],["6796.86","27","0","1"],["6796.88","40","0","1"],["6796.99","5","0","1"],["6797","189","0","4"],["6797.02","22","0","1"],["6797.24","2","0","2"],["6797.25","1","0","1"],["6797.69","34","0","1"],["6797.72","1","0","1"],["6797.78","304","0","1"],["6797.8","1","0","1"],["6797.94","5","0","1"],["6797.98","5","0","1"],["6797.99","100","0","1"],["6798","3411","0","6"],["6798.34","22","0","1"],["6798.49","1000","0","1"],["6798.5","1223","0","1"],["6798.55","60","0","1"],["6798.6","22","0","1"]],"bids":[["6773.4","29","0","4"],["6773.3","3","0","1"],["6773.29","15","0","1"],["6772.94","1","0","1"],["6772.92","3","0","1"],["6772.89","34","0","2"],["6772.85","1","0","1"],["6772.8","1","0","1"],["6772.79","1","0","1"],["6772.58","40","0","1"],["6772.31","1","0","1"],["6772.24","1","0","1"],["6772.15","1","0","1"],["6772.01","9","0","9"],["6772","2239","0","4"],["6771.97","1","0","1"],["6771.85","20","0","1"],["6771.76","1","0","1"],["6771.71","1","0","1"],["6771.7","1","0","1"],["6771.59","24","0","1"],["6771.58","21","0","2"],["6771.57","1","0","1"],["6771.54","1","0","1"],["6771.43","1","0","1"],["6771.4","1","0","1"],["6771.39","1","0","1"],["6771.37","2","0","2"],["6771.23","1","0","1"],["6771.17","20","0","1"],["6771.13","5","0","1"],["6771.06","1","0","1"],["6771.02","5","0","1"],["6771","21","0","2"],["6770.94","303","0","2"],["6770.93","200","0","1"],["6770.91","20","0","1"],["6770.87","291","0","1"],["6770.78","5","0","1"],["6770.75","5","0","1"],["6770.71","60","0","2"],["6770.67","1","0","1"],["6770.65","50","0","1"],["6770.64","38","0","1"],["6770.63","1","0","1"],["6770.62","17","0","1"],["6770.61","44","0","1"],["6770.58","39","0","1"],["6770.57","9","0","1"],["6770.5","20","0","1"],["6770.49","50","0","1"],["6770.48","2","0","2"],["6770.45","6","0","1"],["6770.43","1","0","1"],["6770.42","19","0","2"],["6770.4","12","0","2"],["6770.36","9","0","2"],["6770.35","33","0","2"],["6770.32","67","0","1"],["6770.2","51","0","1"],["6770.17","2","0","2"],["6770.16","3","0","3"],["6770.14","3","0","1"],["6770.12","1","0","1"],["6770.11","64","0","2"],["6770.07","1","0","1"],["6770.06","14","0","1"],["6770.04","3","0","1"],["6770.03","129","0","5"],["6770.02","4","0","1"],["6770.01","56","0","1"],["6770","353","0","4"],["6769.99","4","0","1"],["6769.92","1","0","1"],["6769.9","1","0","1"],["6769.74","37","0","1"],["6769.73","56","0","2"],["6769.7","14","0","1"],["6769.69","14","0","1"],["6769.68","3","0","1"],["6769.66","50","0","1"],["6769.64","1","0","1"],["6769.61","11","0","1"],["6769.6","2","0","2"],["6769.59","50","0","1"],["6769.55","11","0","1"],["6769.51","2","0","2"],["6769.5","2","0","1"],["6769.47","3","0","2"],["6769.4","54","0","3"],["6769.38","4","0","1"],["6769.35","1","0","1"],["6769.33","4","0","1"],["6769.23","3","0","1"],["6769.22","23","0","2"],["6769.16","1","0","1"],["6769.1","50","0","1"],["6769.07","45","0","2"],["6769.04","50","0","1"],["6769","5","0","2"],["6768.99","11","0","1"],["6768.98","17","0","1"],["6768.97","1","0","1"],["6768.95","10","0","1"],["6768.94","1","0","1"],["6768.91","18","0","2"],["6768.88","20","0","1"],["6768.86","17","0","1"],["6768.8","18","0","1"],["6768.78","2","0","1"],["6768.77","5","0","1"],["6768.74","7","0","6"],["6768.73","71","0","6"],["6768.64","1","0","1"],["6768.61","1","0","1"],["6768.6","20","0","1"],["6768.57","1","0","1"],["6768.56","2","0","2"],["6768.53","50","0","1"],["6768.52","1","0","1"],["6768.48","201","0","2"],["6768.47","1","0","1"],["6768.45","335","0","2"],["6768.43","88","0","2"],["6768.42","56","0","4"],["6768.39","2","0","1"],["6768.36","1","0","1"],["6768.34","2","0","1"],["6768.33","50","0","1"],["6768.3","10","0","2"],["6768.25","20","0","2"],["6768.2","14","0","1"],["6768.19","1","0","1"],["6768.17","13","0","1"],["6768.16","3","0","1"],["6768.13","1","0","1"],["6768.12","70","0","2"],["6768.1","826","0","2"],["6768.09","73","0","1"],["6768.03","12","0","1"],["6768.01","14","0","1"],["6768","57","0","3"],["6767.98","1","0","1"],["6767.97","89","0","1"],["6767.89","94","0","1"],["6767.88","2","0","2"],["6767.85","132","0","1"],["6767.83","1","0","1"],["6767.82","1","0","1"],["6767.81","1","0","1"],["6767.76","1","0","1"],["6767.73","124","0","9"],["6767.72","2","0","2"],["6767.71","10","0","1"],["6767.69","1","0","1"],["6767.65","1","0","1"],["6767.62","11","0","2"],["6767.6","1","0","1"],["6767.53","1","0","1"],["6767.52","1","0","1"],["6767.47","105","0","1"],["6767.45","20","0","3"],["6767.43","7","0","2"],["6767.41","67","0","1"],["6767.4","12","0","3"],["6767.39","6","0","1"],["6767.38","199","0","3"],["6767.37","1","0","1"],["6767.33","42","0","3"],["6767.31","1","0","1"],["6767.25","4","0","1"],["6767.21","4","0","1"],["6767.19","37","0","1"],["6767.17","3","0","1"],["6767.15","21","0","1"],["6767.14","46","0","1"],["6767.13","176","0","1"],["6767.12","4","0","2"],["6767.1","83","0","1"],["6767.06","1","0","1"],["6767","404","0","4"],["6766.99","6","0","3"],["6766.98","111","0","1"],["6766.94","7","0","2"],["6766.93","100","0","1"],["6766.92","50","0","1"],["6766.89","4","0","1"],["6766.87","2","0","1"],["6766.85","127","0","1"],["6766.82","40","0","1"],["6766.8","1","0","1"],["6766.73","11","0","1"],["6766.72","5","0","1"],["6766.66","6","0","1"],["6766.65","148","0","1"],["6766.62","4","0","1"],["6766.61","6","0","1"],["6766.6","13","0","1"],["6766.58","17","0","2"],["6766.56","174","0","2"],["6766.52","50","0","1"],["6766.51","1","0","1"],["6766.49","32","0","1"],["6766.48","138","0","1"],["6766.47","121","0","1"],["6766.46","20","0","1"],["6766.4","17","0","2"],["6766.39","48","0","2"],["6766.37","9","0","1"],["6766.31","33","0","2"],["6766.3","38","0","2"],["6766.29","161","0","2"],["6766.28","11","0","1"],["6766.27","9","0","2"],["6766.22","4","0","1"],["6766.19","11","0","1"],["6766.18","1","0","1"],["6766.16","12","0","1"],["6766.11","1","0","1"],["6766.06","2","0","1"],["6766.02","2","0","1"],["6766","3","0","1"],["6765.99","50","0","1"],["6765.98","18","0","1"],["6765.96","181","0","1"],["6765.92","186","0","1"],["6765.87","50","0","1"],["6765.85","11","0","1"],["6765.81","1","0","1"],["6765.77","1","0","1"],["6765.67","50","0","1"],["6765.58","4","0","1"],["6765.56","170","0","1"],["6765.54","4","0","1"],["6765.49","65","0","1"],["6765.47","1","0","1"],["6765.39","201","0","2"],["6765.16","250","0","1"],["6765.06","43","0","3"],["6765.01","50","0","1"],["6765","121","0","4"],["6764.93","56","0","1"],["6764.87","50","0","1"],["6764.84","11","0","2"],["6764.81","1","0","1"],["6764.8","2","0","1"],["6764.77","1","0","1"],["6764.71","10","0","1"],["6764.7","1","0","1"],["6764.67","14","0","1"],["6764.62","1","0","1"],["6764.6","40","0","1"],["6764.57","1","0","1"],["6764.55","1","0","1"],["6764.31","1","0","1"],["6764.3","174","0","2"],["6764.24","30","0","3"],["6764.2","57","0","1"],["6764.14","1","0","1"],["6764.05","26","0","3"],["6764.01","20","0","1"],["6764","3","0","1"],["6763.86","473","0","1"],["6763.82","1","0","1"],["6763.77","1","0","1"],["6763.64","1","0","1"],["6763.62","50","0","1"],["6763.59","1","0","1"],["6763.58","16","0","1"],["6763.48","10","0","1"],["6763.44","2","0","1"],["6763.38","1","0","1"],["6763.33","52","0","2"],["6763.31","50","0","1"],["6763.28","50","0","1"],["6763.25","18","0","1"],["6763.14","12","0","1"],["6763.1","51","0","1"],["6763.05","5","0","1"],["6763","3","0","1"],["6762.97","100","0","2"],["6762.96","50","0","1"],["6762.95","100","0","2"],["6762.94","50","0","1"],["6762.83","51","0","2"],["6762.82","3","0","1"],["6762.8","10","0","1"],["6762.77","3","0","1"],["6762.76","1","0","1"],["6762.66","7","0","2"],["6762.61","50","0","1"],["6762.58","1","0","1"],["6762.52","51","0","2"],["6762.48","50","0","1"],["6762.46","192","0","1"],["6762.28","197","0","1"],["6762.21","30","0","1"],["6762.12","264","0","1"],["6762.01","16","0","1"],["6762","143","0","3"],["6761.84","1","0","1"],["6761.82","194","0","1"],["6761.8","1","0","1"],["6761.78","1","0","1"],["6761.73","54","0","1"],["6761.66","5","0","1"],["6761.61","1","0","1"],["6761.59","1","0","1"],["6761.56","1","0","1"],["6761.55","200","0","1"],["6761.53","1","0","1"],["6761.47","1","0","1"],["6761.22","20","0","1"],["6761","70","0","2"],["6760.92","65","0","4"],["6760.88","1","0","1"],["6760.82","1076","0","1"],["6760.66","1","0","1"],["6760.64","50","0","1"],["6760.49","1","0","1"],["6760.32","4","0","1"],["6760","70","0","2"],["6759.47","2","0","1"],["6759.34","3","0","1"],["6759.3","1","0","1"],["6759.29","5","0","1"],["6759.27","4","0","1"],["6759.13","1","0","1"],["6759.12","50","0","1"],["6759","3","0","1"],["6758.95","15","0","1"],["6758.81","1","0","1"],["6758.77","53","0","1"],["6758.66","1","0","1"],["6758.46","201","0","2"],["6758.28","33","0","1"],["6758.27","11","0","1"],["6758","206","0","2"],["6757.89","65","0","1"],["6757.86","48","0","1"],["6757.77","27","0","1"],["6757.69","50","0","1"],["6757.65","22","0","1"],["6757.62","1","0","1"],["6757.38","79","0","2"],["6757.36","199","0","1"],["6757.12","30","0","1"],["6757.11","1","0","1"],["6757.09","27","0","1"],["6757.06","34","0","1"],["6757.03","22","0","1"],["6757","3","0","1"],["6756.96","33","0","1"],["6756.94","20","0","1"],["6756.76","33","0","1"],["6756.67","1","0","1"],["6756.6","22","0","1"],["6756.44","1","0","1"],["6756.38","40","0","2"],["6756.3","22","0","1"],["6756.2","9","0","1"],["6756.08","3","0","1"],["6756.03","40","0","1"],["6756","3","0","1"],["6755.94","34","0","1"],["6755.84","1","0","1"],["6755.78","211","0","1"],["6755.7","20","0","1"],["6755.68","2","0","1"],["6755.59","5","0","1"],["6755.5","1","0","1"],["6755.49","20","0","1"],["6755.27","4","0","1"],["6755.22","15","0","1"],["6755.14","1","0","1"],["6755","53","0","2"],["6754.98","45","0","1"],["6754.9","1","0","1"],["6754.6","24","0","1"],["6754.54","7","0","1"],["6754.42","13","0","1"],["6754.41","1","0","1"],["6754.37","14","0","1"],["6754.36","1","0","1"],["6754.31","22","0","1"],["6754.26","1","0","1"],["6754.22","19","0","1"],["6754.21","1","0","1"],["6754.14","45","0","1"],["6754.09","6","0","1"],["6754.02","34","0","1"],["6754","3","0","1"],["6753.95","33","0","1"],["6753.89","1","0","1"],["6753.79","372","0","1"],["6753.4","23","0","1"],["6753.23","45","0","2"],["6753.21","1","0","1"],["6753","48","0","2"],["6752.97","22","0","1"]],"timestamp":"2020-04-12T10:24:19.913Z","checksum":854586422}]}
			// {"table":"futures/depth_l2_tbt","action":"update","data":[{"instrument_id":"BTC-USD-200626","asks":[["6773.3","0","0","0"],["6773.39","0","0","0"]],"bids":[["6774.41","0","0","0"],["6773.51","0","0","0"],["6773.42","0","0","0"]],"timestamp":"2020-04-12T10:24:19.925Z","checksum":854586422}]}
			// {"table":"futures/depth_l2_tbt","action":"update","data":[{"instrument_id":"BTC-USD-200626","asks":[["6772.8","0","0","0"],["6777.55","0","0","0"],["6781.25","0","0","0"],["6782.5","11","0","1"],["6783.14","171","0","2"]],"bids":[["6774.21","0","0","0"],["6774.01","0","0","0"],["6773.42","0","0","0"],["6766.01","133","0","1"],["6025.23","0","0","0"],["6025.18","0","0","0"]],"timestamp":"2020-04-12T10:24:19.938Z","checksum":854586422}]}
			// action := ret.Get("action").String()
			var depthL2 WSDepthL2TbtResult
			err := json.Unmarshal(msg, &depthL2)
			if err != nil {
				log.Printf("%v", err)
				return
			}

			if ws.depthL2TbtCallback != nil {
				ws.depthL2TbtCallback(depthL2.Action, depthL2.Data)
			}
			return
		} else if table == TableFuturesTicker {
			var tickerResult WSTickerResult
			err := json.Unmarshal(msg, &tickerResult)
			if err != nil {
				log.Printf("%v", err)
				return
			}

			if ws.tickersCallback != nil {
				ws.tickersCallback(tickerResult.Data)
			}
			return
		} else if table == TableFuturesTrade {
			var tradeResult WSTradeResult
			err := json.Unmarshal(msg, &tradeResult)
			if err != nil {
				log.Printf("%v", err)
				return
			}

			if ws.tradesCallback != nil {
				ws.tradesCallback(tradeResult.Data)
			}
			return
		} else if table == TableFuturesAccount {
			var accountResult WSAccountResult
			err := json.Unmarshal(msg, &accountResult)
			if err != nil {
				log.Printf("%v", err)
				return
			}

			if ws.accountCallback != nil {
				var accounts []WSAccount
				for _, v := range accountResult.Data {
					if v.BTC != nil {
						accounts = append(accounts, *v.BTC)
						continue
					}
					if v.ETH != nil {
						accounts = append(accounts, *v.ETH)
						continue
					}
					if v.ETC != nil {
						accounts = append(accounts, *v.ETC)
						continue
					}
					if v.XRP != nil {
						accounts = append(accounts, *v.XRP)
						continue
					}
					if v.EOS != nil {
						accounts = append(accounts, *v.EOS)
						continue
					}
					if v.BCH != nil {
						accounts = append(accounts, *v.BCH)
						continue
					}
					if v.BSV != nil {
						accounts = append(accounts, *v.BSV)
						continue
					}
					if v.TRX != nil {
						accounts = append(accounts, *v.TRX)
						continue
					}
				}
				ws.accountCallback(accounts)
			}
			return
		} else if table == TableFuturesPosition {
			var positionResult WSFuturesPositionResult
			err := json.Unmarshal(msg, &positionResult)
			if err != nil {
				log.Printf("%v", err)
				return
			}

			if ws.positionCallback != nil {
				ws.positionCallback(positionResult.Data)
			}
			return
		} else if table == TableFuturesOrder {
			var orderResult WSOrderResult
			err := json.Unmarshal(msg, &orderResult)
			if err != nil {
				log.Printf("%v", err)
				return
			}

			if ws.orderCallback != nil {
				ws.orderCallback(orderResult.Data)
			}
			return
		}
		log.Printf("%v", string(msg))
		return
	}

	if eventValue := ret.Get("event"); eventValue.Exists() {
		event := eventValue.String()
		if event == "error" {
			log.Printf("error: %v", string(msg))
			return
		}
		log.Printf("%v", string(msg))
		return
	}

	log.Printf("%v", string(msg))
}

// NewFuturesWS 创建合约WS
// wsURL:
// wss://real.okex.com:8443/ws/v3
func NewFuturesWS(wsURL string, accessKey string, secretKey string, passphrase string) *FuturesWS {
	ws := &FuturesWS{
		wsURL:         wsURL,
		accessKey:     accessKey,
		secretKey:     secretKey,
		passphrase:    passphrase,
		subscriptions: make(map[string]interface{}),
	}
	ws.ctx, ws.cancel = context.WithCancel(context.Background())
	ws.wsConn = recws.RecConn{
		KeepAliveTimeout: 10 * time.Second,
	}
	ws.wsConn.SubscribeHandler = ws.subscribeHandler
	return ws
}

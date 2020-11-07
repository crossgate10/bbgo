// flashcrash strategy tries to place the orders at 30%~50% of the current price,
// so that you can catch the orders while flashcrash happens
package flashcrash

import (
	"context"

	log "github.com/sirupsen/logrus"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/indicator"
	"github.com/c9s/bbgo/pkg/types"
)

func init() {
	bbgo.RegisterStrategy("flashcrash", &Strategy{})
}

type Strategy struct {
	// These fields will be filled from the config file (it translates YAML to JSON)
	// Symbol is the symbol of market you want to run this strategy
	Symbol string `json:"symbol"`

	// Interval is the interval used to trigger order updates
	Interval types.Interval `json:"interval"`

	// GridNum is the grid number, how many orders you want to places
	GridNum int `json:"gridNumber"`

	Percentage float64 `json:"percentage"`

	// BaseQuantity is the quantity you want to submit for each order.
	BaseQuantity float64 `json:"baseQuantity"`

	// activeOrders is the locally maintained active order book of the maker orders.
	activeOrders *bbgo.LocalActiveOrderBook

	// Injection fields start
	// --------------------------
	// Market stores the configuration of the market, for example, VolumePrecision, PricePrecision, MinLotSize... etc
	// This field will be injected automatically since we defined the Symbol field.
	types.Market

	// StandardIndicatorSet contains the standard indicators of a market (symbol)
	// This field will be injected automatically since we defined the Symbol field.
	*bbgo.StandardIndicatorSet
	// --------------------------

	// ewma is the exponential weighted moving average indicator
	ewma *indicator.EWMA
}


func (s *Strategy) updateOrders(orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) {
	if err := session.Exchange.CancelOrders(context.Background(), s.activeOrders.Bids.Orders()...); err != nil {
		log.WithError(err).Errorf("cancel order error")
	}

	s.updateBidOrders(orderExecutor, session)
}

func (s *Strategy) updateBidOrders(orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) {
	quoteCurrency := s.Market.QuoteCurrency
	balances := session.Account.Balances()

	balance, ok := balances[quoteCurrency]
	if !ok || balance.Available <= 0.0 {
		return
	}

	var numOrders = s.GridNum - s.activeOrders.NumOfBids()
	if numOrders <= 0 {
		return
	}

	var startPrice = s.ewma.Last() * s.Percentage

	var submitOrders []types.SubmitOrder
	for i := 0; i < numOrders; i++ {
		submitOrders = append(submitOrders, types.SubmitOrder{
			Symbol:      s.Symbol,
			Side:        types.SideTypeBuy,
			Type:        types.OrderTypeLimit,
			Market:      s.Market,
			Quantity:    s.BaseQuantity,
			Price:       startPrice,
			TimeInForce: "GTC",
		})

		startPrice *= s.Percentage
	}

	orders, err := orderExecutor.SubmitOrders(context.Background(), submitOrders...)
	if err != nil {
		log.WithError(err).Error("submit bid order error")
		return
	}

	s.activeOrders.Add(orders...)
}

func (s *Strategy) orderUpdateHandler(order types.Order) {
	if order.Symbol != s.Symbol {
		return
	}

	log.Infof("received order update: %+v", order)

	switch order.Status {
	case types.OrderStatusFilled:
		s.activeOrders.Delete(order)

	case types.OrderStatusCanceled, types.OrderStatusRejected:
		log.Infof("order status %s, removing %d from the active order pool...", order.Status, order.OrderID)
		s.activeOrders.Delete(order)

	case types.OrderStatusPartiallyFilled:
		s.activeOrders.Add(order)

	default:
		s.activeOrders.Add(order)
	}
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: string(s.Interval)})
}

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	// we don't persist orders so that we can not clear the previous orders for now. just need time to support this.
	s.activeOrders = bbgo.NewLocalActiveOrderBook()
	s.ewma = s.StandardIndicatorSet.GetEWMA(types.IntervalWindow{
		Interval: s.Interval,
		Window:   25,
	})

	session.Stream.OnOrderUpdate(s.orderUpdateHandler)
	session.Stream.OnKLineClosed(func(kline types.KLine) {
		s.updateOrders(orderExecutor, session)
	})

	// TODO: move this to the stream onConnect handler
	s.updateOrders(orderExecutor, session)
	return nil
}
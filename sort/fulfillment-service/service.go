package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/preslavmihaylov/ordertocubby"
	"github.com/vincentkrustanov/go_sort/tree/master/sort/gen"
)

//TODO: functional tests with grpc mocks
//TODO: implement the error handling with the provided structure

const cubbiesCnt = 10

func startWorker(work func([]*gen.Order)) chan []*gen.Order {
	workCh := make(chan []*gen.Order)
	go func() {
		for {
			orders := <-workCh
			work(orders)
		}
	}()
	return workCh
}

func newFulfillmentService(client gen.SortingRobotClient) gen.FulfillmentServer {
	ffs := &fulfillmentService{
		sortingRobot:      client,
		usedCubbies:       make(map[string]bool),
		ordersCountStatus: make(map[string]*fullfillmentCountStatus),
		lock:              sync.RWMutex{},
	}

	ffs.workCh = startWorker(func(orders []*gen.Order) {
		ffs.processOrders(orders)
	})

	return ffs
}

type fullfillmentCountStatus struct {
	orderStatus             *gen.FullfillmentStatus
	numberOfSuccessfulPicks int
}

func (fcs *fullfillmentCountStatus) incrementSuccessfulPicks() {
	fcs.numberOfSuccessfulPicks++
	fcs.updateStatus()
}

func (fcs *fullfillmentCountStatus) updateStatus() {
	if fcs.numberOfSuccessfulPicks == len(fcs.orderStatus.Order.Items) {
		fcs.orderStatus.State = gen.OrderState_READY
	}
}

type fulfillmentService struct {
	sortingRobot      gen.SortingRobotClient
	workCh            chan []*gen.Order
	usedCubbies       map[string]bool
	ordersCountStatus map[string]*fullfillmentCountStatus
	lock              sync.RWMutex
}

func (fs *fulfillmentService) processItemsToCubbies(ctx context.Context, itemsToOrders map[string]string, ordersToCubbies map[string]string, orders []*gen.Order) error {
	for _, order := range orders {
		for _, item := range order.Items {
			_ = item

			resp, err := fs.sortingRobot.PickItem(ctx, &gen.Empty{})
			if err != nil {
				return fmt.Errorf("pick item failed: %v", err)
			}

			cubbyID, err := getCubbyForItem(resp.Item, itemsToOrders)
			if err != nil {
				return fmt.Errorf("process items to cubbies failed: %v", err)
			}

			_, err = fs.sortingRobot.PlaceInCubby(ctx, &gen.PlaceInCubbyRequest{
				Cubby: &gen.Cubby{Id: cubbyID},
			})
			if err != nil {
				return fmt.Errorf("place in cubby failed: %v", err)
			}
			time.Sleep(3 * time.Second)

			fs.lock.Lock()
			fs.ordersCountStatus[itemsToOrders[resp.Item.Code]].incrementSuccessfulPicks()
			fs.lock.Unlock()
		}
	}
	return nil
}

func (fs *fulfillmentService) GetOrderStatusById(ctx context.Context, req *gen.OrderIdRequest) (*gen.OrdersStatusResponse, error) {
	var result gen.OrdersStatusResponse
	fs.lock.Lock()
	defer fs.lock.Unlock()
	result.Status = append(result.Status, fs.ordersCountStatus[req.OrderId].orderStatus)
	return &result, nil
}
func (fs *fulfillmentService) GetAllOrdersStatus(context.Context, *gen.Empty) (*gen.OrdersStatusResponse, error) {
	var result gen.OrdersStatusResponse
	fs.lock.Lock()
	defer fs.lock.Unlock()
	for _, order := range fs.ordersCountStatus {
		result.Status = append(result.Status, order.orderStatus)
	}
	return &result, nil
}
func (fs *fulfillmentService) MarkFullfilled(context.Context, *gen.OrderIdRequest) (*gen.Empty, error) {
	return nil, nil
}

func (fs *fulfillmentService) LoadOrders(ctx context.Context, in *gen.LoadOrdersRequest) (*gen.CompleteResponse, error) {

	go func() {
		fs.workCh <- in.Orders
	}()

	return &gen.CompleteResponse{}, nil
}

func (fs *fulfillmentService) processOrders(orders []*gen.Order) {
	itemsToOrders := mapItemsToOrders(orders)
	ordersToCubbies := fs.mapOrdersToCubbies(orders)
	ctx := context.Background()
	fs.processItemsToCubbies(ctx, itemsToOrders, ordersToCubbies, orders)
}

func (fs *fulfillmentService) mapOrdersToCubbies(orders []*gen.Order) map[string]string {
	ordersToCubbies := map[string]string{}

	for _, order := range orders {
		cubbyID := mapOrderToCubby(fs.usedCubbies, order.Id, cubbiesCnt)
		ordersToCubbies[order.Id] = cubbyID
		fs.usedCubbies[cubbyID] = true

		fs.lock.Lock()
		fs.ordersCountStatus[order.Id] = &fullfillmentCountStatus{
			orderStatus: &gen.FullfillmentStatus{
				Cubby: &gen.Cubby{Id: cubbyID},
				Order: order,
				State: gen.OrderState_PENDING,
			},
			numberOfSuccessfulPicks: 0,
		}
		fs.lock.Unlock()
	}

	for orderID, cubbyID := range ordersToCubbies {
		fmt.Printf("order %s -> cubby %s\n", orderID, cubbyID)
	}

	return ordersToCubbies
}

func mapItemsToOrders(orders []*gen.Order) map[string]string {
	itemsToOrders := map[string]string{}
	for _, order := range orders {
		for _, item := range order.Items {
			itemsToOrders[item.Code] = order.Id
		}
	}
	return itemsToOrders
}

func mapOrderToCubby(usedCubbies map[string]bool, id string, cubbiesCnt int) string {
	times := 1
	for {
		cubbyID := ordertocubby.Map(id, uint32(times), uint32(cubbiesCnt))
		if !usedCubbies[cubbyID] {
			return cubbyID
		}
		times++
	}
}

func getCubbyForItem(item *gen.Item, itemsToOrders map[string]string) (string, error) {
	orderId := itemsToOrders[item.Code]
	if orderId == "" {
		return orderId, fmt.Errorf("item %s -> %s not found", item.Code, item.Label)
	}
	return orderId, nil
}

package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/brocaar/lorawan"
	"github.com/pkg/errors"

	"github.com/brocaar/loraserver/api/as"
	"github.com/brocaar/loraserver/internal/common"
	"github.com/brocaar/loraserver/internal/test"
	. "github.com/smartystreets/goconvey/convey"
)

func TestDeviceQueue(t *testing.T) {
	conf := test.GetConfig()
	db, err := common.OpenDatabase(conf.PostgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	common.DB = db

	Convey("Given a clean database", t, func() {
		test.MustResetDB(common.DB)
		asClient := test.NewApplicationClient()
		common.ApplicationServerPool = test.NewApplicationServerPool(asClient)

		Convey("Given a service, device and routing profile and device", func() {
			sp := ServiceProfile{}
			So(CreateServiceProfile(db, &sp), ShouldBeNil)

			dp := DeviceProfile{}
			So(CreateDeviceProfile(db, &dp), ShouldBeNil)

			rp := RoutingProfile{}
			So(CreateRoutingProfile(db, &rp), ShouldBeNil)

			d := Device{
				DevEUI:           lorawan.EUI64{1, 2, 3, 4, 5, 6, 7, 8},
				ServiceProfileID: sp.ServiceProfile.ServiceProfileID,
				DeviceProfileID:  dp.DeviceProfile.DeviceProfileID,
				RoutingProfileID: rp.RoutingProfile.RoutingProfileID,
			}
			So(CreateDevice(db, &d), ShouldBeNil)

			Convey("Given a set of queue items", func() {
				now := time.Now().UTC().Truncate(time.Millisecond)
				inOneHour := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)

				items := []DeviceQueueItem{
					{
						DevEUI:     d.DevEUI,
						FRMPayload: []byte{1, 2, 3},
						FCnt:       1,
						FPort:      10,
						Confirmed:  true,
						RetryCount: 3,
					},
					{
						DevEUI:     d.DevEUI,
						FRMPayload: []byte{4, 5, 6},
						FCnt:       3,
						FPort:      11,
						EmitAt:     &now,
					},
					{
						DevEUI:      d.DevEUI,
						FRMPayload:  []byte{7, 8, 9},
						FCnt:        2,
						FPort:       12,
						EmitAt:      &inOneHour,
						ForwardedAt: &now,
					},
				}
				for i := range items {
					So(CreateDeviceQueueItem(db, &items[i]), ShouldBeNil)
					items[i].CreatedAt = items[i].UpdatedAt.UTC().Truncate(time.Millisecond)
					items[i].UpdatedAt = items[i].UpdatedAt.UTC().Truncate(time.Millisecond)
				}

				Convey("Then GetDeviceQueueItem returns the requested item", func() {
					qi, err := GetDeviceQueueItem(db, items[0].ID)
					So(err, ShouldBeNil)
					qi.CreatedAt = qi.CreatedAt.UTC().Truncate(time.Millisecond)
					qi.UpdatedAt = qi.UpdatedAt.UTC().Truncate(time.Millisecond)
					So(qi, ShouldResemble, items[0])
				})

				Convey("Then UpdateDeviceQueueItem updates the queue item", func() {
					items[0].RetryCount = 2
					items[0].ForwardedAt = &now
					So(UpdateDeviceQueueItem(db, &items[0]), ShouldBeNil)
					items[0].UpdatedAt = items[0].UpdatedAt.UTC().Truncate(time.Millisecond)

					qi, err := GetDeviceQueueItem(db, items[0].ID)
					So(err, ShouldBeNil)
					emittedAt := qi.ForwardedAt.UTC()
					qi.CreatedAt = qi.CreatedAt.UTC().Truncate(time.Millisecond)
					qi.UpdatedAt = qi.UpdatedAt.UTC().Truncate(time.Millisecond)
					qi.ForwardedAt = &emittedAt
					So(qi, ShouldResemble, items[0])
				})

				Convey("Then GetDeviceQueueItemsForDevEUI returns the expected items in the expected order", func() {
					queueItems, err := GetDeviceQueueItemsForDevEUI(db, d.DevEUI)
					So(err, ShouldBeNil)
					So(queueItems, ShouldHaveLength, len(items))
					So(queueItems[0].FCnt, ShouldEqual, 1)
					So(queueItems[1].FCnt, ShouldEqual, 2)
					So(queueItems[2].FCnt, ShouldEqual, 3)
				})

				Convey("Then GetNextDeviceQueueItemForDevEUI returns the first item that should be emitted", func() {
					qi, err := GetNextDeviceQueueItemForDevEUI(db, d.DevEUI)
					So(err, ShouldBeNil)
					So(qi.FCnt, ShouldEqual, 1)
				})

				Convey("Then FlushDeviceQueueForDevEUI flushes the queue", func() {
					So(FlushDeviceQueueForDevEUI(db, d.DevEUI), ShouldBeNil)
					items, err := GetDeviceQueueItemsForDevEUI(db, d.DevEUI)
					So(err, ShouldBeNil)
					So(items, ShouldHaveLength, 0)
				})

				Convey("Then DeleteDeviceQueueItem deletes a queue item", func() {
					So(DeleteDeviceQueueItem(db, items[0].ID), ShouldBeNil)
					items, err := GetDeviceQueueItemsForDevEUI(db, d.DevEUI)
					So(err, ShouldBeNil)
					So(items, ShouldHaveLength, 2)
				})
			})

			Convey("When testing GetNextDeviceQueueItemForDevEUIMaxPayloadSizeAndFCnt", func() {
				items := []DeviceQueueItem{
					{
						DevEUI:     d.DevEUI,
						FCnt:       100,
						FPort:      1,
						FRMPayload: []byte{1, 2, 3, 4, 5, 6, 7},
						RetryCount: -1,
					},
					{
						DevEUI:     d.DevEUI,
						FCnt:       101,
						FPort:      1,
						FRMPayload: []byte{1, 2, 3, 4, 5, 6, 7},
					},
					{
						DevEUI:     d.DevEUI,
						FCnt:       102,
						FPort:      2,
						FRMPayload: []byte{1, 2, 3, 4, 5, 6},
					},
					{
						DevEUI:     d.DevEUI,
						FCnt:       103,
						FPort:      3,
						FRMPayload: []byte{1, 2, 3, 4, 5},
					},
					{
						DevEUI:     d.DevEUI,
						FCnt:       104,
						FPort:      4,
						FRMPayload: []byte{1, 2, 3, 4},
					},
				}
				for i := range items {
					So(CreateDeviceQueueItem(common.DB, &items[i]), ShouldBeNil)
				}

				tests := []struct {
					Name          string
					FCnt          uint32
					MaxFRMPayload int

					ExpectedDeviceQueueItemID *int64
					ExpectedHandleError       []as.HandleErrorRequest
					ExpectedHandleDownlinkACK []as.HandleDownlinkACKRequest
					ExpectedError             error
				}{
					{
						Name:                      "nACK + first item from the queue (payload size)",
						FCnt:                      100,
						MaxFRMPayload:             7,
						ExpectedDeviceQueueItemID: &items[1].ID,
						ExpectedHandleDownlinkACK: []as.HandleDownlinkACKRequest{
							{DevEUI: d.DevEUI[:], FCnt: items[0].FCnt, Acknowledged: false},
						},
					},
					{
						Name:                      "nACK + first item discarded (payload size)",
						FCnt:                      100,
						MaxFRMPayload:             6,
						ExpectedDeviceQueueItemID: &items[1].ID,
						ExpectedHandleError: []as.HandleErrorRequest{
							{DevEUI: d.DevEUI[:], Type: as.ErrorType_DEVICE_QUEUE_ITEM_SIZE, Error: "payload exceeds max payload size", FCnt: 101},
						},
						ExpectedHandleDownlinkACK: []as.HandleDownlinkACKRequest{
							{DevEUI: d.DevEUI[:], FCnt: items[0].FCnt, Acknowledged: false},
						},
					},
					{
						Name:                      "nACK + first two items discarded (payload size)",
						FCnt:                      100,
						MaxFRMPayload:             5,
						ExpectedDeviceQueueItemID: &items[1].ID,
						ExpectedHandleError: []as.HandleErrorRequest{
							{DevEUI: d.DevEUI[:], Type: as.ErrorType_DEVICE_QUEUE_ITEM_SIZE, Error: "payload exceeds max payload size", FCnt: 101},
							{DevEUI: d.DevEUI[:], Type: as.ErrorType_DEVICE_QUEUE_ITEM_SIZE, Error: "payload exceeds max payload size", FCnt: 102},
						},
						ExpectedHandleDownlinkACK: []as.HandleDownlinkACKRequest{
							{DevEUI: d.DevEUI[:], FCnt: items[0].FCnt, Acknowledged: false},
						},
					},
					{
						Name:          "nACK + all items discarded (payload size)",
						FCnt:          101,
						MaxFRMPayload: 3,
						ExpectedHandleError: []as.HandleErrorRequest{
							{DevEUI: d.DevEUI[:], Type: as.ErrorType_DEVICE_QUEUE_ITEM_SIZE, Error: "payload exceeds max payload size", FCnt: 101},
							{DevEUI: d.DevEUI[:], Type: as.ErrorType_DEVICE_QUEUE_ITEM_SIZE, Error: "payload exceeds max payload size", FCnt: 102},
							{DevEUI: d.DevEUI[:], Type: as.ErrorType_DEVICE_QUEUE_ITEM_SIZE, Error: "payload exceeds max payload size", FCnt: 103},
							{DevEUI: d.DevEUI[:], Type: as.ErrorType_DEVICE_QUEUE_ITEM_SIZE, Error: "payload exceeds max payload size", FCnt: 104},
						},
						ExpectedHandleDownlinkACK: []as.HandleDownlinkACKRequest{
							{DevEUI: d.DevEUI[:], FCnt: items[0].FCnt, Acknowledged: false},
						},
						ExpectedError: ErrDoesNotExist,
					},
					{
						Name:                      "nACK + first item discarded (fCnt)",
						FCnt:                      102,
						MaxFRMPayload:             7,
						ExpectedDeviceQueueItemID: &items[1].ID,
						ExpectedHandleError: []as.HandleErrorRequest{
							{DevEUI: d.DevEUI[:], Type: as.ErrorType_DEVICE_QUEUE_ITEM_FCNT, Error: "frame-counter exceeds MaxFCntGap", FCnt: 101},
						},
						ExpectedHandleDownlinkACK: []as.HandleDownlinkACKRequest{
							{DevEUI: d.DevEUI[:], FCnt: items[0].FCnt, Acknowledged: false},
						},
					},
				}

				for i, test := range tests {
					Convey(fmt.Sprintf("Testing: %s [%d]", test.Name, i), func() {
						qi, err := GetNextDeviceQueueItemForDevEUIMaxPayloadSizeAndFCnt(common.DB, d.DevEUI, test.MaxFRMPayload, test.FCnt, rp.RoutingProfileID)
						if test.ExpectedHandleError == nil {
							So(*test.ExpectedDeviceQueueItemID, ShouldEqual, qi.ID)
							So(err, ShouldBeNil)
						} else {
							So(errors.Cause(err), ShouldEqual, test.ExpectedError)
						}

						So(asClient.HandleErrorChan, ShouldHaveLength, len(test.ExpectedHandleError))
						for _, err := range test.ExpectedHandleError {
							req := <-asClient.HandleErrorChan
							So(req, ShouldResemble, err)
						}

						So(asClient.HandleDownlinkACKChan, ShouldHaveLength, len(test.ExpectedHandleDownlinkACK))
						for _, ack := range test.ExpectedHandleDownlinkACK {
							req := <-asClient.HandleDownlinkACKChan
							So(req, ShouldResemble, ack)
						}
					})
				}
			})
		})
	})
}

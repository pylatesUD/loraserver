package storage

import (
	"context"
	"time"

	"github.com/brocaar/loraserver/api/as"

	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/brocaar/loraserver/internal/common"
	"github.com/brocaar/lorawan"
)

// DeviceQueueItem represents an item in the device queue (downlink).
type DeviceQueueItem struct {
	ID          int64         `db:"id"`
	CreatedAt   time.Time     `db:"created_at"`
	UpdatedAt   time.Time     `db:"updated_at"`
	DevEUI      lorawan.EUI64 `db:"dev_eui"`
	FRMPayload  []byte        `db:"frm_payload"`
	FCnt        uint32        `db:"f_cnt"`
	FPort       uint8         `db:"f_port"`
	Confirmed   bool          `db:"confirmed"`
	EmitAt      *time.Time    `db:"emit_at"`
	ForwardedAt *time.Time    `db:"forwarded_at"`
	RetryCount  int           `db:"retry_count"`
}

// CreateDeviceQueueItem adds the given item to the device queue.
func CreateDeviceQueueItem(db sqlx.Queryer, qi *DeviceQueueItem) error {
	now := time.Now()
	qi.CreatedAt = now
	qi.UpdatedAt = now

	err := sqlx.Get(db, &qi.ID, `
        insert into device_queue (
            created_at,
            updated_at,
            dev_eui,
            frm_payload,
            f_cnt,
            f_port,
            confirmed,
            emit_at,
            forwarded_at,
            retry_count
        ) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
        returning id`,
		qi.CreatedAt,
		qi.UpdatedAt,
		qi.DevEUI[:],
		qi.FRMPayload,
		qi.FCnt,
		qi.FPort,
		qi.Confirmed,
		qi.EmitAt,
		qi.ForwardedAt,
		qi.RetryCount,
	)
	if err != nil {
		return handlePSQLError(err, "insert error")
	}

	log.WithFields(log.Fields{
		"dev_eui": qi.DevEUI,
		"id":      qi.ID,
	}).Info("device-queue item created")

	return nil
}

// GetDeviceQueueItem returns the device-queue item matching the given id.
func GetDeviceQueueItem(db sqlx.Queryer, id int64) (DeviceQueueItem, error) {
	var qi DeviceQueueItem
	err := sqlx.Get(db, &qi, "select * from device_queue where id = $1", id)
	if err != nil {
		return qi, handlePSQLError(err, "select error")
	}
	return qi, nil
}

// UpdateDeviceQueueItem updates the given device-queue item.
func UpdateDeviceQueueItem(db sqlx.Execer, qi *DeviceQueueItem) error {
	qi.UpdatedAt = time.Now()

	res, err := db.Exec(`
        update device_queue
        set
            updated_at = $2,
            dev_eui = $3,
            frm_payload = $4,
            f_cnt = $5,
            f_port = $6,
            confirmed = $7,
            emit_at = $8,
            forwarded_at = $9,
            retry_count = $10
        where
            id = $1`,
		qi.ID,
		qi.UpdatedAt,
		qi.DevEUI[:],
		qi.FRMPayload,
		qi.FCnt,
		qi.FPort,
		qi.Confirmed,
		qi.EmitAt,
		qi.ForwardedAt,
		qi.RetryCount,
	)
	if err != nil {
		return handlePSQLError(err, "update error")
	}
	ra, err := res.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "get rows affected error")
	}
	if ra == 0 {
		return ErrDoesNotExist
	}

	log.WithFields(log.Fields{
		"id":      qi.ID,
		"dev_eui": qi.DevEUI,
	}).Info("device-queue item updated")

	return nil
}

// DeleteDeviceQueueItem deletes the device-queue item matching the given id.
func DeleteDeviceQueueItem(db sqlx.Execer, id int64) error {
	res, err := db.Exec("delete from device_queue where id = $1", id)
	if err != nil {
		return handlePSQLError(err, "delete error")
	}
	ra, err := res.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "get rows affected error")
	}
	if ra == 0 {
		return ErrDoesNotExist
	}

	log.WithFields(log.Fields{
		"id": id,
	}).Info("device-queue deleted")

	return nil
}

// FlushDeviceQueueForDevEUI deletes all device-queue items for the given DevEUI.
func FlushDeviceQueueForDevEUI(db sqlx.Execer, devEUI lorawan.EUI64) error {
	_, err := db.Exec("delete from device_queue where dev_eui = $1", devEUI[:])
	if err != nil {
		return handlePSQLError(err, "delete error")
	}

	log.WithFields(log.Fields{
		"dev_eui": devEUI,
	}).Info("device-queue flushed")

	return nil
}

// GetNextDeviceQueueItemForDevEUI returns the next device-queue item for the
// given DevEUI, sorted by FCnt (asc).
func GetNextDeviceQueueItemForDevEUI(db sqlx.Queryer, devEUI lorawan.EUI64) (DeviceQueueItem, error) {
	var qi DeviceQueueItem
	err := sqlx.Get(db, &qi, `
        select
            *
        from
            device_queue
        where
            dev_eui = $1
        order by
            f_cnt
        limit 1`,
		devEUI[:],
	)
	if err != nil {
		return qi, handlePSQLError(err, "select error")
	}

	return qi, nil
}

// GetDeviceQueueItemsForDevEUI returns all device-queue items for the given
// DevEUI (sorted by FCnt asc).
func GetDeviceQueueItemsForDevEUI(db sqlx.Queryer, devEUI lorawan.EUI64) ([]DeviceQueueItem, error) {
	var items []DeviceQueueItem
	err := sqlx.Select(db, &items, `
        select
            *
        from
            device_queue
        where
            dev_eui = $1
        order by
            f_cnt`,
		devEUI,
	)
	if err != nil {
		return nil, handlePSQLError(err, "select error")
	}

	return items, nil
}

// GetNextDeviceQueueItemForDevEUIMaxPayloadSizeAndFCnt returns the next
// device-queue for the given DevEUI item respecting:
// * maxPayloadSize: the maximum payload size
// * fCnt: the current expected frame-counter
// In case the payload exceeds the max payload size or when the payload
// frame-counter - the given fCnt > MaxFCntGap, the payload will be removed
// from the queue and the next one will be retrieved. In such a case, the
// application-server will be notified.
func GetNextDeviceQueueItemForDevEUIMaxPayloadSizeAndFCnt(db sqlx.Ext, devEUI lorawan.EUI64, maxPayloadSize int, fCnt uint32, routingProfileID string) (DeviceQueueItem, error) {
	for {
		qi, err := GetNextDeviceQueueItemForDevEUI(db, devEUI)
		if err != nil {
			return DeviceQueueItem{}, errors.Wrap(err, "get next device-queue item error")
		}

		if qi.FCnt-fCnt > common.Band.MaxFCntGap || len(qi.FRMPayload) > maxPayloadSize || qi.RetryCount < 0 {
			rp, err := GetRoutingProfile(db, routingProfileID)
			if err != nil {
				return DeviceQueueItem{}, errors.Wrap(err, "get routing-profile error")
			}
			asClient, err := common.ApplicationServerPool.Get(rp.ASID)
			if err != nil {
				return DeviceQueueItem{}, errors.Wrap(err, "get application-server client error")
			}

			if err := DeleteDeviceQueueItem(db, qi.ID); err != nil {
				return DeviceQueueItem{}, errors.Wrap(err, "delete device-queue item error")
			}

			if qi.RetryCount < 0 {
				// number of re-tries exceeded
				log.WithFields(log.Fields{
					"dev_eui":                devEUI,
					"device_queue_item_fcnt": qi.FCnt,
				}).Warning("device-queue item discarded due to max number of re-tries")

				_, err = asClient.HandleDownlinkACK(context.Background(), &as.HandleDownlinkACKRequest{
					DevEUI:       devEUI[:],
					FCnt:         qi.FCnt,
					Acknowledged: false,
				})
				if err != nil {
					return DeviceQueueItem{}, errors.Wrap(err, "application-server client error")
				}
			} else if qi.FCnt-fCnt > common.Band.MaxFCntGap {
				// handle frame-counter error
				log.WithFields(log.Fields{
					"dev_eui":                devEUI,
					"device_session_fcnt":    fCnt,
					"device_queue_item_fcnt": qi.FCnt,
				}).Warning("device-queue item discarded due to invalid fCnt")

				_, err = asClient.HandleError(context.Background(), &as.HandleErrorRequest{
					DevEUI: devEUI[:],
					Type:   as.ErrorType_DEVICE_QUEUE_ITEM_FCNT,
					FCnt:   qi.FCnt,
					Error:  "frame-counter exceeds MaxFCntGap",
				})
				if err != nil {
					return DeviceQueueItem{}, errors.Wrap(err, "application-server client error")
				}
			} else if len(qi.FRMPayload) > maxPayloadSize {
				// handle max payload size error
				log.WithFields(log.Fields{
					"device_queue_item_fcnt":         qi.FCnt,
					"dev_eui":                        devEUI,
					"max_payload_size":               maxPayloadSize,
					"device_queue_item_payload_size": len(qi.FRMPayload),
				}).Warning("device-queue item discarded as it exceeds the max payload size")

				_, err = asClient.HandleError(context.Background(), &as.HandleErrorRequest{
					DevEUI: devEUI[:],
					Type:   as.ErrorType_DEVICE_QUEUE_ITEM_SIZE,
					FCnt:   qi.FCnt,
					Error:  "payload exceeds max payload size",
				})
				if err != nil {
					return DeviceQueueItem{}, errors.Wrap(err, "application-server client error")
				}
			}

			// try next frame
			continue
		}

		return qi, nil
	}
}

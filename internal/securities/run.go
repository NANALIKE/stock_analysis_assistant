package securities

import (
	"fmt"
	"time"
)

// ClosedDate rejects future/unclosed targets before invoking a supplier.
func ClosedDate(to string, now time.Time) error {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	local := now.In(location)
	today := local.Format(time.DateOnly)
	if to > today || (to == today && local.Hour() < 15) {
		return fmt.Errorf("target must not be later than the latest closed trading day")
	}
	return nil
}

func Expected(instrument Instrument, day string) bool {
	// The outDate itself is provisional: daily membership/bar evidence resolves it.
	return instrument.ListedOn <= day && (instrument.DelistedOn == nil || day <= *instrument.DelistedOn)
}

package auth

import "time"

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func billingAnniversary(anchor time.Time, months int) time.Time {
	anchor = anchor.UTC().Truncate(time.Second)
	return monthlyOnDay(anchor, anchor.Day(), months)
}

func monthlyOnDay(at time.Time, day, months int) time.Time {
	at = at.UTC().Truncate(time.Second)
	monthStart := time.Date(at.Year(), at.Month()+time.Month(months), 1, at.Hour(), at.Minute(), at.Second(), 0, time.UTC)
	day = min(day, daysInMonth(monthStart.Year(), monthStart.Month()))
	return monthStart.AddDate(0, 0, day-1)
}

func billingAnniversariesAround(anchor, at time.Time) (atOrBefore, after time.Time) {
	anchor = anchor.UTC()
	at = at.UTC()
	months := (at.Year()-anchor.Year())*12 + int(at.Month()-anchor.Month()) - 1
	for {
		candidate := billingAnniversary(anchor, months)
		if candidate.After(at) {
			return atOrBefore, candidate
		}
		atOrBefore = candidate
		months++
	}
}

func NextBillingAnniversary(anchor, after time.Time) time.Time {
	_, next := billingAnniversariesAround(anchor, after)
	return next
}

func nearestBillingAnniversary(anchor, at time.Time) time.Time {
	before, after := billingAnniversariesAround(anchor, at)
	if at.Sub(before) < after.Sub(at) {
		return before
	}
	return after
}

func oneMonthLater(at time.Time) time.Time {
	return billingAnniversary(at, 1)
}

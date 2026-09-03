package runtimeusage

import (
	"errors"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxQueryDays     = 366
	maxTimeZoneBytes = 128
	usageDateLayout  = time.DateOnly
)

var ErrInvalidQuery = errors.New("Runtime usage query is invalid")

// Period is the exact civil-time window represented by a Report. From is
// inclusive and Until is exclusive. TimeZone is an IANA time-zone identifier;
// retaining it prevents a calendar cell from becoming ambiguous when the
// Runtime Server and the operator are in different zones.
type Period struct {
	From     string `json:"from"`
	Until    string `json:"until"`
	TimeZone string `json:"timeZone"`
}

// Query is a validated civil-time usage window. Its instant boundaries remain
// private so callers cannot construct a window whose labels disagree with its
// filtering semantics.
type Query struct {
	period   Period
	from     time.Time
	until    time.Time
	location *time.Location
}

func NewQuery(from, until, timeZone string) (Query, error) {
	if !validTimeZoneName(timeZone) {
		return Query{}, ErrInvalidQuery
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return Query{}, ErrInvalidQuery
	}
	fromTime, err := time.ParseInLocation(usageDateLayout, from, location)
	if err != nil || fromTime.Format(usageDateLayout) != from {
		return Query{}, ErrInvalidQuery
	}
	untilTime, err := time.ParseInLocation(usageDateLayout, until, location)
	if err != nil || untilTime.Format(usageDateLayout) != until || !untilTime.After(fromTime) {
		return Query{}, ErrInvalidQuery
	}
	days := 0
	for cursor := fromTime; cursor.Before(untilTime); cursor = cursor.AddDate(0, 0, 1) {
		days++
		if days > MaxQueryDays {
			return Query{}, ErrInvalidQuery
		}
	}
	return Query{
		period: Period{From: from, Until: until, TimeZone: timeZone},
		from:   fromTime, until: untilTime, location: location,
	}, nil
}

func (query Query) valid() bool {
	return query.location != nil && query.period.From != "" &&
		query.period.Until != "" && query.period.TimeZone != "" &&
		query.until.After(query.from)
}

func (query Query) contains(value time.Time) bool {
	return !value.Before(query.from) && value.Before(query.until)
}

func (query Query) day(value time.Time) string {
	return value.In(query.location).Format(usageDateLayout)
}

func (query Query) Period() Period { return query.period }

func validTimeZoneName(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > maxTimeZoneBytes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

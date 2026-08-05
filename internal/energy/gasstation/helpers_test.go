package gasstation

import (
	"errors"
	"strconv"
)

func errorsAs(err error, target any) bool { return errors.As(err, target) }

func itoa(v int) string { return strconv.Itoa(v) }

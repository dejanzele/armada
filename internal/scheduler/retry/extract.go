package retry

import (
	"github.com/armadaproject/armada/pkg/armadaevents"
)

func extractCategory(err *armadaevents.Error) string {
	return err.GetFailureCategory()
}

func extractSubcategory(err *armadaevents.Error) string {
	return err.GetFailureSubcategory()
}

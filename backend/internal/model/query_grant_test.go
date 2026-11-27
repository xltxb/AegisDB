package model

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGrantReasonSizeMatchesColumns(t *testing.T) {
	want := "size:" + strconv.Itoa(GrantReasonSize)
	for _, v := range []any{QueryGrant{}, QueryGrantRequest{}} {
		f, _ := reflect.TypeOf(v).FieldByName("Reason")
		if !strings.Contains(f.Tag.Get("gorm"), want) {
			t.Errorf("%T.Reason 的 gorm 标签 %q 与 GrantReasonSize=%d 不一致", v, f.Tag.Get("gorm"), GrantReasonSize)
		}
	}
}

package gateway

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 分层关掉脱敏(2026-10-29):服务层给 ctx 打上 WithMaskingOff,终端结果与导出流都给原值;
// 不打就照旧打码。
func TestMaskingOff_TerminalAndExportGivePlaintext(t *testing.T) {
	withIDCardRule(t)
	conn := sqliteConn(t)
	const plain = "310101199001011234"
	ctx := context.Background()
	for _, s := range []string{"CREATE TABLE t_person (id INTEGER PRIMARY KEY, id_card TEXT)", "INSERT INTO t_person VALUES (1, '" + plain + "')"} {
		if _, err := RealRun(ctx, conn, s, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	q := "SELECT id, id_card FROM t_person"
	run := func(c context.Context) string {
		r, err := RealRun(c, conn, q, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return r.Data[0][1]
	}
	stream := func(c context.Context) string {
		var got []string
		if err := RealQueryEach(c, conn, q, 5*time.Second, func([]string) error { return nil },
			func(row []string) error { got = append(got, row[1]); return nil }); err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, ",")
	}
	if run(ctx) == plain || stream(ctx) == plain {
		t.Fatal("默认应打码")
	}
	off := WithMaskingOff(ctx)
	if got := run(off); got != plain {
		t.Errorf("终端:关掉脱敏应给原值,实际 %q", got)
	}
	if got := stream(off); got != plain {
		t.Errorf("导出流:关掉脱敏应给原值,实际 %q", got)
	}
}

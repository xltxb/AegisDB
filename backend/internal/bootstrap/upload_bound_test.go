package bootstrap

// 两条上传通道必须在**同一个尺寸**上拒绝。
//
// 控制台(/api/v1/scripts/*)与开放接口(/api/v1/open/*)各自读上传的脚本,两段代码
// 逐字相同,而上限各写了一份常量。同一份逻辑抄两遍的代价不在抄的那一刻,在下一次
// 改的时候:有人把上限调到 30MB,改到的是其中一个 —— 于是同一个文件,从界面传进去
// 被收下,从流水线传进去被拒,而两边的报错都说"15MB 上限"。
//
// 所以这里不问"上限是多少",问的是**两条通道答不答得一样**。答案从哪来无所谓,
// 但必须只有一个来源。

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"
)

// postConsoleMultipart 走控制台那条上传路径。
func postConsoleMultipart(t *testing.T, app *testApp, token, path string, fields map[string]string, fileSize int) apiResp {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", "payload.sql")
	pad := bytes.Repeat([]byte("-- pad\n"), fileSize/7)
	_, _ = fw.Write(pad)
	_, _ = fw.Write(bytes.Repeat([]byte("-"), fileSize-len(pad)))
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, app.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("multipart post: %v", err)
	}
	defer res.Body.Close()
	var env apiResp
	_ = json.NewDecoder(res.Body).Decode(&env)
	return env
}

func TestUploadBound_BothChannelsRefuseAtTheSameSize(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	token := app.issueClient(admin, "CI审查", "zhangwei@vela.io", []string{"review:check"})

	const over = 15<<20 + 1024
	const under = 15<<20 - 1024

	openOver := postOpenMultipart(t, app, token, "/api/v1/open/sql-review",
		map[string]string{"dialect": "mysql"}, over)
	consoleOver := postConsoleMultipart(t, app, admin, "/api/v1/scripts/upload", nil, over)
	if (openOver.Code == 0) != (consoleOver.Code == 0) {
		t.Errorf("同一个超限文件,开放接口 code=%d(%s)、控制台 code=%d(%s) —— 两条通道的上限不一样了",
			openOver.Code, openOver.Msg, consoleOver.Code, consoleOver.Msg)
	}
	if consoleOver.Code == 0 {
		t.Error("控制台收下了超限的脚本")
	}

	openUnder := postOpenMultipart(t, app, token, "/api/v1/open/sql-review",
		map[string]string{"dialect": "mysql"}, under)
	consoleUnder := postConsoleMultipart(t, app, admin, "/api/v1/scripts/upload", nil, under)
	if (openUnder.Code == 0) != (consoleUnder.Code == 0) {
		t.Errorf("同一个未超限文件,开放接口 code=%d(%s)、控制台 code=%d(%s)",
			openUnder.Code, openUnder.Msg, consoleUnder.Code, consoleUnder.Msg)
	}
	if consoleUnder.Code != 0 {
		t.Errorf("控制台拒了一个未超限的脚本:%s", consoleUnder.Msg)
	}
}

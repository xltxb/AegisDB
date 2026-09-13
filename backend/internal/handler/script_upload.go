package handler

// 上传脚本的读取,两条通道共用一份。
//
// 控制台(/api/v1/scripts/*)与开放接口(/api/v1/open/*)各自收上传的脚本文件,从前
// 两段代码逐字相同,上限各写了一份常量,连"15MB"这个数字都各自硬写在文案里。同一
// 份逻辑抄两遍的代价不在抄的那一刻,在下一次改的时候:有人把上限调到 30MB,改到的
// 是其中一个 —— 于是同一个文件,从界面传进去被收下,从流水线传进去被拒,而两边的
// 报错都还说"15MB 上限"。
//
// 现在上限只有一处,文案从它算出来,拒绝的措辞也就不会和实际尺寸分家。

import (
	"fmt"
	"io"
	"mime/multipart"

	"github.com/gin-gonic/gin"

	"velagateway/pkg/resp"
)

// maxScriptUploadBytes 是**两条通道共同的**上传上限。它也对齐服务层对内联脚本的
// 限制,免得一条脚本在门口被收下、却死在元数据库里。
const maxScriptUploadBytes = 15 << 20

// uploadLimitText 把上限写成人话。与常量同源,所以调上限时文案跟着走。
func uploadLimitText() string { return fmt.Sprintf("%dMB", maxScriptUploadBytes>>20) }

// readScriptUpload 在上限内读一个上传的脚本文件。
//
// 不信 fh.Size:那是客户端说的。用 LimitReader 读,读满上限+1 就说明超了 —— 按
// fh.Size 去 make 一个缓冲区,等于让一个撒谎的 Content-Length 决定分配多少内存。
//
// ok=false 表示拒绝已经写进响应了,调用方直接 return。
func readScriptUpload(c *gin.Context, fh *multipart.FileHeader) (string, bool) {
	if fh.Size > maxScriptUploadBytes {
		resp.Fail(c, resp.CodeBadRequest, "脚本超过 "+uploadLimitText()+" 上限")
		return "", false
	}
	f, err := fh.Open()
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "脚本读取失败")
		return "", false
	}
	defer f.Close()
	body, rerr := io.ReadAll(io.LimitReader(f, maxScriptUploadBytes+1))
	if rerr != nil || len(body) > maxScriptUploadBytes {
		resp.Fail(c, resp.CodeBadRequest, "脚本读取失败或超过 "+uploadLimitText()+" 上限")
		return "", false
	}
	return string(body), true
}

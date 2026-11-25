// Package apimsg 让接口报错的中文 msg 带上一个机器可读的身份(msgRef),前端据此按读者的语言重讲一遍。
//
// 这是 RuleRef 机制的第七处应用(前端命名空间 apiMsg.),但做法与前六处不同:那几处的句子不多,
// 每句手起一个 code、手写一个 Render;接口报错有几百句,散在每个 handler / service 里。于是这里
// 不改句子、也不改它们的写法:
//
//   - 固定的中文串(resp.Fail(c, code, "参数错误")、errors.New("连接不存在"))一字不动,出口按原串查表;
//   - 带参数的(fmt.Errorf("第 %d 条…", n))只把 fmt.Errorf 换成同签名的 apimsg.Errorf,
//     中文由同一个格式串、同一组参数经 fmt 生成,所以中文用户看到的仍是原来那句,一字不差。
//
// code 是中文原串(格式串)的哈希。中文改了 code 就跟着变,英文词条随之失配 —— 这是故意的:
// 测试会报「en.json5 缺词条」,逼着改中文的人同时改英文,而不是让英文界面悄悄显示一句过时的译文。
//
// 文案表(catalog_gen.go)与前端 zh / en 词条由 `go generate ./internal/apimsg` 从源码扫描生成,
// 见 scan 包;测试保证三者与源码一致。
package apimsg

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"aegisdb/internal/model"
)

//go:generate go run ./gen

// Raw 是嵌在句子里的「不翻译的原文」:数据库驱动的报错、没有收录的错误。前端原样插入。
const Raw = "raw"

// Slot 是 part 的保留参数名:这一段填进外层句子的第几个占位符。
const Slot = "_slot"

// Entry 是文案表的一条:一句中文;Fmt 表示它是 fmt 格式串(参数按 %d / %s 位置填进去)。
type Entry struct {
	Zh  string
	Fmt bool
}

// Code 是一句中文的 code:前端词条键 apiMsg.<code>。
func Code(zh string) string {
	h := sha1.Sum([]byte(zh))
	return "m" + hex.EncodeToString(h[:4])
}

var known = func() map[string]bool {
	m := make(map[string]bool, len(catalog))
	for _, e := range catalog {
		m[Code(e.Zh)] = true
	}
	return m
}()

// Lookup 认出一句收录过的固定中文,返回它的 ref;没收录(驱动原文、英文、空串)返回 nil。
func Lookup(msg string) *model.RuleRef {
	if msg == "" || !known[Code(msg)] {
		return nil
	}
	return &model.RuleRef{Code: Code(msg)}
}

// Error 是带 ref 的错误。Error() 就是 fmt.Errorf 生成的那句中文;Unwrap 保留 %w 链,errors.Is 照旧。
type Error struct {
	err error
	ref *model.RuleRef
}

func (e *Error) Error() string { return e.err.Error() }
func (e *Error) Unwrap() error { return e.err }

// Ref 返回这句话的 ref(格式串没收录时为 nil)。
func (e *Error) Ref() *model.RuleRef { return e.ref }

// Errorf 与 fmt.Errorf 完全同义(含 %w),另外记下格式串与参数,供界面翻译。
func Errorf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	if !known[Code(format)] {
		return err // 没收录:测试会拦下,运行时退化为纯中文
	}
	ref := buildRef(format, args)
	if ref == nil {
		return err
	}
	return &Error{err: err, ref: ref}
}

// Text 标记一句会作为接口报错出现、但不是 errors.New / Errorf 写出来的固定中文
// (如拒绝原因的 Reason 字段)。原样返回;它只是让扫描把这句收进文案表。
func Text(s string) string { return s }

// Refer 由自己拼 Error() 的错误类型实现,交出与 Error() 同一句话的 ref。
type Refer interface {
	MsgRef() *model.RuleRef
}

// RefOf 取一个错误的 ref:最外层就是 apimsg 错误(或实现了 Refer),或者整句是收录过的固定中文。
//
// 里层包着 apimsg 错误、外面又套了一层没收录的话时不用里层的 ref —— 那样英文界面
// 只会显示里层那半句,外层的信息就丢了。宁可整句显示中文原串。
func RefOf(err error) *model.RuleRef {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) && e.Error() == err.Error() {
		return e.ref
	}
	var r Refer
	if errors.As(err, &r) {
		if re, ok := r.(error); ok && re.Error() == err.Error() {
			if ref := r.MsgRef(); ref != nil {
				return ref
			}
		}
	}
	return Lookup(err.Error())
}

// M 是一句不是报错的话(通知、任务错误、终端输出、提示):S 是规范中文,Ref 是它的身份
// (格式串没收录时为 nil)。作为 Errorf / Sprintf 的参数时嵌成子句,%s 打印 S。
type M struct {
	S   string
	Ref *model.RuleRef
}

func (m M) String() string { return m.S }

// Sprintf 与 fmt.Sprintf 同义,另外记下格式串与参数,供界面翻译。
func Sprintf(format string, args ...any) M {
	m := M{S: fmt.Sprintf(format, args...)}
	if known[Code(format)] {
		m.Ref = buildRef(format, args)
	}
	return m
}

// T 是一句固定的话(收录进文案表),等同 Sprintf 不带参数。
func T(s string) M { return M{S: s, Ref: Lookup(s)} }

// Of 包一句运行时才确定的话(已在别处用 Text / T 收录,或干脆不认识):认得就带 ref。
func Of(s string) M { return M{S: s, Ref: Lookup(s)} }

// FromErr 把一个错误当成一句话:S 是 err.Error(),ref 同 RefOf。
func FromErr(err error) M { return M{S: err.Error(), Ref: RefOf(err)} }

// LineRef 是一段多行文本里某一行的身份:T 是那一行的原文,R 是它的 ref。界面逐行比对原文,
// 对得上的行换成译文 —— 不按行号对位,文本被截掉开头(日志只留末尾)也不会错位。
type LineRef struct {
	T string         `json:"t"`
	R *model.RuleRef `json:"r"`
}

// LinesJSON 把这些行(只收带 ref 的)编成存库用的 JSON;一行都没有时存空串。
func LinesJSON(lines []M) string {
	out := make([]LineRef, 0, len(lines))
	for _, l := range lines {
		if l.Ref != nil && l.S != "" {
			out = append(out, LineRef{T: l.S, R: l.Ref})
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// RefJSON 把 ref 编成存库用的 JSON;nil 存空串(读出时再按原句查表)。
func RefJSON(r *model.RuleRef) string {
	if r == nil {
		return ""
	}
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

func init() {
	// 存库文本读出时补 ref(model 的 AfterFind 钩子用,见 model/text_ref_fill.go)。
	model.StoredTextRef = func(text, refJSON string) string {
		if refJSON != "" {
			return refJSON
		}
		return RefJSON(Lookup(text))
	}
}

// StoredRef 是存库文本读出来时的 ref:写入时存了 ref 就用它,否则按原句查表 ——
// 固定句的老数据因此也能翻译;带参数的老数据没有 ref,界面显示中文原句。
func StoredRef(text, refJSON string) *model.RuleRef {
	if refJSON != "" {
		var r model.RuleRef
		if json.Unmarshal([]byte(refJSON), &r) == nil && r.Code != "" {
			return &r
		}
	}
	return Lookup(text)
}

// partOf 把作为参数嵌进句子的错误变成一个 part:能认出就是它的 ref,认不出就是原文。
func partOf(err error, slot int) model.RuleRef {
	return partFrom(err.Error(), RefOf(err), slot)
}

func partFrom(text string, r *model.RuleRef, slot int) model.RuleRef {
	p := model.RuleRef{Code: Raw, Args: map[string]string{"text": text}}
	if r != nil {
		p = model.RuleRef{Code: r.Code, Args: map[string]string{}, Parts: r.Parts}
		for k, v := range r.Args {
			p.Args[k] = v
		}
	}
	p.Args[Slot] = strconv.Itoa(slot)
	return p
}

func buildRef(format string, args []any) *model.RuleRef {
	verbs, ok := ParseVerbs(format)
	if !ok || len(verbs) != len(args) {
		return nil
	}
	ref := &model.RuleRef{Code: Code(format), Args: map[string]string{}}
	for i, v := range verbs {
		a := args[i]
		if err, isErr := a.(error); isErr && err != nil {
			ref.Parts = append(ref.Parts, partOf(err, i))
		} else if m, isM := a.(M); isM {
			ref.Parts = append(ref.Parts, partFrom(m.S, m.Ref, i))
		}
		spec := v
		if spec[len(spec)-1] == 'w' {
			spec = spec[:len(spec)-1] + "v"
		}
		ref.Args[strconv.Itoa(i)] = fmt.Sprintf(spec, a)
	}
	if len(ref.Args) == 0 {
		ref.Args = nil
	}
	return ref
}

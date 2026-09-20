# 全站 HUD 视觉升级 · 设计

日期：2026-09-20 · 状态：待实现

## 要解决的问题

`docs/_ds/vela-design-system-*/readme.md` 定义的 Vela 视觉语言是「亮色年轻有力、暗色未来霓虹」，
签名效果写得很清楚：azure→cyan 品牌渐变、`--glow-sm/--glow-md` 霓虹辉光、顶栏 `backdrop-filter`、
大数字渐变裁字、按钮 press 下沉、暗色靠边框与辉光而不是投影。

token 层全都备齐了（`src/styles/` 下 7 个文件），**落地层几乎没吃到**：

| 设计系统要求 | 现状（实测 `src/styles/theme.css`） |
|---|---|
| 主 CTA hover 带 glow | `.c-btn.v-primary:hover` 只换了个底色 |
| 按钮 press `translateY(1px) scale(.99)` | 没有 `:active` 规则 |
| 顶栏 `backdrop-filter: blur(8px)` + 半透明 | `.top` 是 `--surface-card` 实色 |
| 大数字渐变裁字 | `.dash-nums b`、`.us-statv` 是纯 `--text-strong` |
| 卡片 `elevation="glow"` | `.c-card` 只有 1px 描边 + 纯色底 |
| 品牌渐变 | 全站只用了两次：`.rail-logo`、`.rail-avatar` |
| `--glow-sm` / `--glow-md` | 全站引用 1 次（`.portal-opt.on`） |
| `--blur-md` / `--blur-lg` | 引用 0 次 |

结果是：一套为「科技感」设计的 token，渲染出来是一个中性、扁平、任何 SaaS 都长这样的控制台。

**目标**：把已有的视觉语言用满，并补上一层 HUD 界面底座（网格、辉光、角标、活体状态），
让亮色像蓝图 / 测量仪、暗色像监控大屏。亮暗两套都做到一等主题。

## 非目标

- **不动信息架构、不动布局尺寸。** 栏宽、行距、断点、列数一律不改。
  `theme.css` 里那些写明「为什么是 7px / 为什么是 64px / 为什么只有两个断点」的注释所保护的决定，
  全部保留。本轮只加装饰层。
- **不动文字色与语义状态色 token。** `--text-*`、`--success/warning/danger` 不改，对比度不退化。
- **不加斑马线。** 审计页一屏 22 行是刻意压出来的密度，斑马会把它搅乱。
- **不做手机端专门形态。** 沿用 `2026-09-16-responsive-layout-design.md` 的两断点口径。
- **不引入新依赖。** 纯 CSS，不加动画库、不加图片资源。

## 一、文件边界

方案是「token 扩展 + 共用层就地改 + 新增装饰层」。三处各管一件事，任一条规则只出现在一个地方，
**不存在同选择器的覆盖关系**——这是为了避免第二套真相与权重战争。

| 位置 | 职责 |
|---|---|
| `src/styles/colors.css` · `effects.css` | 新增 HUD 语义 token，亮/暗各一套 |
| `src/styles/theme.css` 共用段（约 30–550 行） | **就地改**既有骨架声明：`.c-card` `.c-btn` `.c-table` `.c-badge` `.top` `.rail` `.login-*` `:focus-visible` |
| **新建** `src/styles/hud.css` | 只放**新增的装饰层**：页面网格底座、卡片角标与高光线、脉冲 keyframes。全部走伪元素与新类 |

`hud.css` 由 `main.tsx` 在 `theme.css` **之后** import：

```ts
import '@/styles/theme.css'
import '@/styles/hud.css'
```

不塞进 `theme.css` 的 `@import` 块——CSS 的 `@import` 必须位于所有规则之前，
放在那里 `hud.css` 会被后面 2800 行同权重规则压过去。

## 二、Token 层

追加到 `colors.css` 的 `:root` 与 `[data-theme="dark"]` 两处（HUD 段落，与既有语义段分开）：

| token | 亮色 | 暗色 | 用途 |
|---|---|---|---|
| `--hud-grid` | `rgba(105,116,139,.09)` | `rgba(255,255,255,.045)` | 网格线。亮色取 slate-500 稀释，是冷灰蓝不是黑 |
| `--hud-glow-a` | `rgba(59,110,246,.07)` | `rgba(59,110,246,.16)` | 页面径向辉光 · azure |
| `--hud-glow-b` | `rgba(45,205,230,.06)` | `rgba(45,205,230,.12)` | 页面径向辉光 · cyan |
| `--hud-corner` | `rgba(59,110,246,.30)` | `rgba(45,205,230,.42)` | L 形角标线 |
| `--hud-hairline` | `rgba(59,110,246,.45)` | `rgba(94,131,251,.75)` | 顶部高光线的浓端 |

追加到 `effects.css` 的 `:root`：

```
--hud-grid-size: 32px;                 /* 4px 栅格 ×8 */
--hud-edge: linear-gradient(90deg, transparent, var(--hud-hairline),
            color-mix(in oklch, var(--glow-accent) 70%, transparent), transparent);
--hud-corner-size: 12px;
```

**亮色辉光压到 6–7%**：它的作用是把四角从死白里兜住，不是让人看见一团蓝。
超过 10% 在白底上会变成明显色斑，卡片的白就不干净了。

## 三、共用层：9 条规则

### 1. 页面底座（`hud.css`）

网格 + 双径向辉光挂在 `.main::before`，不是滚动容器 `.content`：

```css
.main { position: relative; isolation: isolate; }
.main > * { position: relative; z-index: 1; }
.main::before {
  content: ''; position: absolute; inset: 0; z-index: 0; pointer-events: none;
  background:
    radial-gradient(60% 50% at 88% 0%,   var(--hud-glow-b), transparent 70%),
    radial-gradient(50% 45% at 0% 100%,  var(--hud-glow-a), transparent 70%),
    linear-gradient(var(--hud-grid) 1px, transparent 1px) 0 0 / 100% var(--hud-grid-size),
    linear-gradient(90deg, var(--hud-grid) 1px, transparent 1px) 0 0 / var(--hud-grid-size) 100%;
}
```

**挂 `.main` 不挂 `.content`**：`.content` 是 `overflow:auto` 的滚动容器，背景挂在它身上
会随内容滚动并在每帧重绘整张渐变；`.main` 是固定高度的 flex 列，背景画一次就不动了——
而且固定不动的网格才像 HUD 底座，跟着内容滚的网格像壁纸。

`.login` 同样处理（它不在 `.main` 里，是独立的全屏 grid）。

### 2. 卡片（`theme.css` 骨架 + `hud.css` 装饰）

`.c-card` / `.dash-card` / `.c-table` 三者共用一组装饰。它们都已经有
`border-radius:14px` + `overflow:hidden`，角标画在内侧不会被裁掉。

- `::before` —— 顶部 1px 渐变高光线（`--hud-edge`），`left:0;right:0;top:0`。
- `::after` —— 右上角 `12px×12px` 的 L 形角标：`border-top` + `border-right`，色 `--hud-corner`，
  距边 `10px`。

只做两个角，不做四个：一个元素只有两个伪元素，四角要么加 DOM、要么把角标画进 `background-image`
而 `.c-card` 的 `background` 常被页面级 CSS 覆盖（如 `.us-stat.warning`）。右上一个角足够给出
「这是一块仪表面板」的读感。登录页是唯一的例外，见第四节。

**`.c-table` 必须改用 `background-image`，不能用伪元素。** 它是 `overflow-x:auto` 的横滚容器
（列宽合计超出时横滚，见该处注释），而绝对定位的后代会跟着内容一起滚——1440 宽的审计表往右滚
一屏，高光线和角标就跑到可视区外面去了。背景图的 `background-attachment` 默认是 `scroll`，
定位锚在元素自己的边框盒上，**不随内容滚动**，正好是要的行为：

```css
.c-table {
  background-image:
    linear-gradient(90deg, transparent, var(--hud-hairline),
      color-mix(in oklch, var(--glow-accent) 70%, transparent), transparent);
  background-repeat: no-repeat;
  background-size: 100% 1px;
  background-position: 0 0;
  /* 既有的 background: var(--surface-card) 保留为 background-color */
}
```

角标在 `.c-table` 上省略——两条 `linear-gradient` 拼一个 L 形要四个背景层，
为一个装饰不值得，而表格靠顶部那条线已经分出层了。

**`.perm-rcard` 不加卡片装饰**，只吃第 3 条的 hover 抬升：它的 `::before` 已经被选中态的
左侧色条占用（`theme.css:1060`），是全站唯一的伪元素冲突点。

卡头 `.c-card-head` / `.dash-card > header` 的 `border-bottom` 由纯色改为渐变遮罩
（左浓右淡）：用 `border-image` 会丢圆角，改用 `::after` 画 1px 线更稳，
但卡头的 `::after` 未被占用，可用。

`.c-card-icon` / `.dash-ico`：补 `1px solid var(--accent-subtle-border)` 与
`box-shadow: inset 0 0 12px -4px var(--accent)`。

### 3. 可交互卡的 hover

只给**确实可点**的卡：`.dash-card`、`.perm-rcard`、`.portal-opt`。

`.portal-opt.on` 已经带 `box-shadow: var(--glow-sm)`，hover 的阴影规则要写成
`:hover:not(.on)`，否则选中态的辉光会被 hover 的投影顶掉。

```
transform: translateY(-2px);
亮色 box-shadow: var(--shadow-lg);
暗色 box-shadow: var(--glow-sm);
```

静态卡（设置页、审批详情那些）不加。整站卡片一起飘会显得廉价，而且
「能不能点」是 hover 反馈唯一要回答的问题。

### 4. 指标数字

`.dash-nums b`、`.us-statv`：

```css
background: linear-gradient(135deg, var(--text-strong), var(--accent-text));
-webkit-background-clip: text; background-clip: text; color: transparent;
```

渐变幅度刻意小（strong → accent-text，不是 azure → cyan）：大数字是要读的，
不是要发光的。设计系统允许 big-stat numerals 用 `background-clip:text`，这里用在它规定的位置上。

`.dash-nums .warn b`（告警数字）保持纯 `--warning-text`，不裁字——语义色不能被渐变冲淡。

### 5. 表格

- `.c-thead`：去掉 `background: var(--surface-sunken)` 实底，改 `transparent`；
  底部 `border-bottom` 换成渐变线（`--hud-edge` 的低透明度版本）。mono 大写字距保持不变。
- `.c-trow.clickable:hover`：在既有的 `background: var(--surface-sunken)` 之外，
  加 `box-shadow: inset 2px 0 0 var(--accent)`。

用 `inset` 阴影不用 `border-left`：和 `.rail-item.on`、终端实例树选中态同一个理由——
border 会把整行内容向右挤 2px，hover 时行内文字跳一下。

### 6. 按钮

| 选择器 | 改动 |
|---|---|
| `.c-btn.v-primary` | `background` 改 `linear-gradient(120deg, var(--accent), color-mix(in oklch, var(--glow-accent) 30%, var(--accent)))` |
| `.c-btn.v-primary:hover` | 在换色之外叠 `box-shadow: var(--glow-sm)` |
| `.c-btn:active:not(:disabled)` | **新增** `transform: translateY(1px) scale(.99)` |
| `.c-btn:hover:not(:disabled)` | 在既有变色之外加 `box-shadow: inset 0 0 0 1px var(--accent-subtle-border)` |

渐变只走 azure→cyan，30% 的 cyan 混入量。设计系统明令禁止 azure→violet（「reads generic」）。

`:active` 的 transform 要写进 `prefers-reduced-motion` 的豁免——它是瞬时状态不是动画，
但 `transition` 时长已被 token 统一收到 0，行为自然退化，无需额外处理。

### 7. 徽标与活体状态

- `.c-badge`：补 `border: 1px solid currentColor`，并把 `color` 的 alpha 交给各 tone 的
  `--*-text`；描边用 `color-mix(in oklch, currentColor 28%, transparent)` 免得变成实心框。
- `.pill-health` 的圆点：新增 `@keyframes hud-pulse`（`box-shadow` 由 0 扩到 6px 再收回，2.4s
  infinite），并加 `filter: drop-shadow(0 0 4px var(--glow-accent))`。

脉冲只给「系统健康」这一个位置。全站到处闪等于没有重点，而这一枚是唯一表示「实时在跑」的指示灯。

### 8. 外壳

| 选择器 | 改动 | 理由 |
|---|---|---|
| `.top` | `background: color-mix(in oklch, var(--surface-card) 78%, transparent)` + `backdrop-filter: blur(8px)`；`border-bottom` 改渐变线 | 设计系统对顶栏的原文要求，现在是实色 |
| `.rail` | `border-right` 改为 `::after` 画的渐变竖线（上下淡、中间浓） | 一条从头到尾一样浓的灰线是「隔断」，渐变线是「边缘」 |
| `.rail-item.on` | 既有 `inset 3px 0 0 var(--accent)` 后追加 `, 0 0 16px -6px var(--accent)` | 选中项在暗色下需要一点外溢才跳得出来 |

`.rail` 已经是 `overflow-y:auto` 且有两处 sticky 元素靠 `box-shadow` 补缝
（见该处注释）。改 `border-right` 为伪元素时要确认这两处 sticky 的外扩阴影仍然盖得住——
`::after` 用 `position:sticky` 不可行，改用 `position:absolute; top:0; bottom:0; right:0`
挂在 `.rail` 上，滚动内容不会从它上面过（它在 padding box 外沿）。

### 9. 焦点态

`base.css` 的 `:focus-visible` 由

```css
outline: 2px solid var(--accent); outline-offset: 2px;
```

改为

```css
outline: 2px solid var(--accent); outline-offset: 2px;
box-shadow: 0 0 0 4px var(--focus-ring);
```

`--focus-ring` 已存在且亮暗各有值（暗色 60% azure）。**outline 保留不动**——它是键盘可达性的
底线，光晕只是叠加。

## 四、两个样板页

### 登录页

1. 底座同第三节第 1 条，挂 `.login::before`。
2. `.login-card` 加四角 L 角标。这是**全站唯一破例改 TSX** 的地方：伪元素只够画两个角，
   而登录页是第一印象页，四角完整的取景框读感值这一个装饰元素。
   在 `pages/login/` 的卡片内加：
   ```tsx
   <span className="hud-corners" aria-hidden="true" />
   ```
   `.hud-corners` 在 `hud.css` 里用 `::before`/`::after` + 自身背景画四角，`pointer-events:none`。
3. `.login-mark` 由 `background: var(--accent)` 换成 `linear-gradient(135deg, #3b6ef6, #2dcde6)`
   + `box-shadow: 0 0 18px -4px rgba(45,205,230,.6)`，与 `.rail-logo` 完全一致——
   同一个品牌标在两处不该长得不一样。
4. `.login-card` 加 `backdrop-filter: blur(var(--blur-sm))` 与顶部高光线。

**不加** mono eyebrow 文案（`SECURE DATABASE GATEWAY` 之类）：那要动 i18n 两份 locale，
属于文案变更不是视觉变更，本轮不碰。

### 总览页

不新增页面级规则，靠第三节第 2/3/4 条自动吃到：卡片高光线与角标、hover 抬升、数字渐变裁字、
`.dash-ico` 描边与内发光。这一页的作用是**验证共用层够不够**——如果总览看起来还是平的，
说明共用层规则不到位，应该回去改共用层，而不是给总览写特例。

## 五、验收

### 自动化

| 命令 | 必须通过的理由 |
|---|---|
| `npm run build`（tsc + vite） | 登录页改了 TSX |
| `npm run test:unit` | 基线不回归 |
| `npm run test:e2e` | 两条与本轮直接相关，见下 |

- `e2e/reduced-motion.spec.ts` —— 新增的 `hud-pulse` 必须写进
  `@media (prefers-reduced-motion: reduce) { animation: none }`。
- `e2e/responsive.spec.ts` —— 它断言 390/768/1024/1280/1440 五档下
  `document.scrollWidth` 不溢出、`.page-head` 不内横滚。所有装饰必须
  `pointer-events:none` 且不改变盒模型：角标用 absolute 定位，网格用 background，
  高光线用 absolute 的 1px 伪元素。任何一处用 `border` 或 `padding` 实现都会挪动布局。

### 人工

用 `run-app` skill 跑起来，截图 **亮/暗 × 登录页/总览页** 四张，确认：

1. 亮色下网格与辉光「看得出底座、看不出色斑」——把眼睛移开还以为是白底。
2. 暗色下卡片靠高光线与角标分层，不靠投影（设计系统：暗色 drops drop-shadows）。
3. 总览页的指标数字仍然一眼可读，渐变没有把它冲淡。
4. 表格行 hover 的左竖条出现时，行内文字**没有横向位移**。

四张截图过了，再按同一套规则铺剩下 20 页。

## 六、铺开顺序

1. **第一阶段（本 spec）**：token 层 + `hud.css` + `theme.css` 共用段 + 登录页 + 总览页。
2. **第二阶段（第一阶段验收通过后另起计划）**：其余 20 页的页面级 CSS 按同一套规则对齐，
   重点是那些自建了卡片外观而没复用 `.c-card` 的页面——
   `.perm-roles`、`.us-stat`、`.dash-card` 之外的连接页/终端页/脚本页容器。

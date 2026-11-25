/* @ds-bundle: {"format":3,"namespace":"VelaDesignSystem_c1e10b","components":[{"name":"Alert","sourcePath":"components/core/Alert.jsx"},{"name":"Avatar","sourcePath":"components/core/Avatar.jsx"},{"name":"Badge","sourcePath":"components/core/Badge.jsx"},{"name":"Card","sourcePath":"components/core/Card.jsx"},{"name":"Spinner","sourcePath":"components/core/Spinner.jsx"},{"name":"Tabs","sourcePath":"components/core/Tabs.jsx"},{"name":"Button","sourcePath":"components/forms/Button.jsx"},{"name":"Checkbox","sourcePath":"components/forms/Checkbox.jsx"},{"name":"Input","sourcePath":"components/forms/Input.jsx"},{"name":"Select","sourcePath":"components/forms/Select.jsx"},{"name":"Switch","sourcePath":"components/forms/Switch.jsx"}],"sourceHashes":{"components/core/Alert.jsx":"ffd3df73b5c6","components/core/Avatar.jsx":"6287ef4f8124","components/core/Badge.jsx":"c3674e203f1d","components/core/Card.jsx":"b78fc392e7f5","components/core/Spinner.jsx":"abab6afdc90e","components/core/Tabs.jsx":"e213a61ab017","components/forms/Button.jsx":"edbad0b4fb4e","components/forms/Checkbox.jsx":"3c04fe574056","components/forms/Input.jsx":"a3f860da945e","components/forms/Select.jsx":"18eb391b6e8f","components/forms/Switch.jsx":"93f3e6e417fb","ui_kits/mobile/MobileApp.jsx":"5b32468b6ef4","ui_kits/web/App.jsx":"064badedbab2","ui_kits/web/Chrome.jsx":"e825648aafe4","ui_kits/web/DashboardScreen.jsx":"560557b3410a","ui_kits/web/LoginScreen.jsx":"34ddf94431e9","ui_kits/web/ProjectsScreen.jsx":"be5876cc0a6b"},"inlinedExternals":[],"unexposedExports":[]} */

(() => {

const __ds_ns = (window.VelaDesignSystem_c1e10b = window.VelaDesignSystem_c1e10b || {});

const __ds_scope = {};

(__ds_ns.__errors = __ds_ns.__errors || []);

// components/core/Alert.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-alert{
  display:flex; gap:var(--space-3); padding:var(--space-3) var(--space-4);
  border-radius:var(--radius-md); border:1px solid transparent;
  font-size:var(--text-sm); line-height:var(--leading-snug);
}
.vela-alert__icon{ flex:none; margin-top:1px; display:flex; }
.vela-alert__icon svg{ width:18px; height:18px; }
.vela-alert__body{ flex:1; min-width:0; }
.vela-alert__title{ font-weight:var(--weight-semibold); color:var(--text-strong); margin-bottom:2px; }
.vela-alert--info{ background:var(--accent-subtle); border-color:var(--accent-subtle-border); color:var(--text-body); }
.vela-alert--info .vela-alert__icon{ color:var(--accent-text); }
.vela-alert--success{ background:var(--success-subtle); border-color:color-mix(in oklch, var(--success) 30%, transparent); color:var(--text-body); }
.vela-alert--success .vela-alert__icon{ color:var(--success); }
.vela-alert--warning{ background:var(--warning-subtle); border-color:color-mix(in oklch, var(--warning) 35%, transparent); color:var(--text-body); }
.vela-alert--warning .vela-alert__icon{ color:var(--warning-text); }
.vela-alert--danger{ background:var(--danger-subtle); border-color:color-mix(in oklch, var(--danger) 30%, transparent); color:var(--text-body); }
.vela-alert--danger .vela-alert__icon{ color:var(--danger); }
`;
const ICONS = {
  info: /*#__PURE__*/React.createElement("path", {
    d: "M12 16v-4M12 8h.01M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20z"
  }),
  success: /*#__PURE__*/React.createElement("path", {
    d: "M22 11.08V12a10 10 0 1 1-5.93-9.14M22 4 12 14.01l-3-3"
  }),
  warning: /*#__PURE__*/React.createElement("path", {
    d: "M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0zM12 9v4M12 17h.01"
  }),
  danger: /*#__PURE__*/React.createElement("path", {
    d: "M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20zM15 9l-6 6M9 9l6 6"
  })
};
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-alert-css")) return;
  const el = document.createElement("style");
  el.id = "vela-alert-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Alert({
  tone = "info",
  title,
  className = "",
  children,
  ...rest
}) {
  ensure();
  return /*#__PURE__*/React.createElement("div", _extends({
    className: ["vela-alert", `vela-alert--${tone}`, className].filter(Boolean).join(" "),
    role: "status"
  }, rest), /*#__PURE__*/React.createElement("span", {
    className: "vela-alert__icon",
    "aria-hidden": "true"
  }, /*#__PURE__*/React.createElement("svg", {
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: "2",
    strokeLinecap: "round",
    strokeLinejoin: "round"
  }, ICONS[tone])), /*#__PURE__*/React.createElement("div", {
    className: "vela-alert__body"
  }, title && /*#__PURE__*/React.createElement("div", {
    className: "vela-alert__title"
  }, title), children));
}
Object.assign(__ds_scope, { Alert });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/core/Alert.jsx", error: String((e && e.message) || e) }); }

// components/core/Avatar.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-avatar{
  position:relative; display:inline-flex; align-items:center; justify-content:center;
  flex:none; overflow:hidden; background:var(--accent-subtle); color:var(--accent-text);
  font-family:var(--font-body); font-weight:var(--weight-semibold); user-select:none;
  border-radius:var(--radius-full);
}
.vela-avatar--square{ border-radius:var(--radius-md); }
.vela-avatar img{ width:100%; height:100%; object-fit:cover; }
.vela-avatar--xs{ width:24px; height:24px; font-size:10px; }
.vela-avatar--sm{ width:32px; height:32px; font-size:12px; }
.vela-avatar--md{ width:40px; height:40px; font-size:14px; }
.vela-avatar--lg{ width:56px; height:56px; font-size:20px; }
.vela-avatar__status{
  position:absolute; right:0; bottom:0; width:30%; height:30%; min-width:8px; min-height:8px;
  border-radius:50%; border:2px solid var(--surface-card);
}
.vela-avatar__status--online{ background:var(--success); }
.vela-avatar__status--busy{ background:var(--danger); }
.vela-avatar__status--away{ background:var(--warning); }
.vela-avatar__status--offline{ background:var(--slate-400); }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-avatar-css")) return;
  const el = document.createElement("style");
  el.id = "vela-avatar-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function initials(name = "") {
  const parts = name.trim().split(/\s+/);
  if (!parts[0]) return "?";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}
function Avatar({
  src,
  name = "",
  size = "md",
  shape = "circle",
  status,
  className = "",
  ...rest
}) {
  ensure();
  const cls = ["vela-avatar", `vela-avatar--${size}`, shape === "square" ? "vela-avatar--square" : "", className].filter(Boolean).join(" ");
  return /*#__PURE__*/React.createElement("span", _extends({
    className: cls
  }, rest), src ? /*#__PURE__*/React.createElement("img", {
    src: src,
    alt: name
  }) : /*#__PURE__*/React.createElement("span", null, initials(name)), status && /*#__PURE__*/React.createElement("span", {
    className: `vela-avatar__status vela-avatar__status--${status}`
  }));
}
Object.assign(__ds_scope, { Avatar });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/core/Avatar.jsx", error: String((e && e.message) || e) }); }

// components/core/Badge.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-badge{
  display:inline-flex; align-items:center; gap:var(--space-1-5);
  height:22px; padding:0 var(--space-2); border-radius:var(--radius-full);
  font-family:var(--font-body); font-size:var(--text-xs); font-weight:var(--weight-semibold);
  line-height:1; white-space:nowrap; letter-spacing:var(--tracking-normal);
}
.vela-badge--solid{ color:#fff; }
.vela-badge__dot{ width:6px; height:6px; border-radius:50%; background:currentColor; }
.vela-badge--neutral.vela-badge--soft{ background:var(--surface-sunken); color:var(--text-body); }
.vela-badge--neutral.vela-badge--solid{ background:var(--slate-600); }
.vela-badge--accent.vela-badge--soft{ background:var(--accent-subtle); color:var(--accent-text); }
.vela-badge--accent.vela-badge--solid{ background:var(--accent); }
.vela-badge--success.vela-badge--soft{ background:var(--success-subtle); color:var(--success-text); }
.vela-badge--success.vela-badge--solid{ background:var(--success); }
.vela-badge--warning.vela-badge--soft{ background:var(--warning-subtle); color:var(--warning-text); }
.vela-badge--warning.vela-badge--solid{ background:var(--warning); }
.vela-badge--danger.vela-badge--soft{ background:var(--danger-subtle); color:var(--danger-text); }
.vela-badge--danger.vela-badge--solid{ background:var(--danger); }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-badge-css")) return;
  const el = document.createElement("style");
  el.id = "vela-badge-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Badge({
  color = "neutral",
  variant = "soft",
  dot = false,
  className = "",
  children,
  ...rest
}) {
  ensure();
  const cls = ["vela-badge", `vela-badge--${color}`, `vela-badge--${variant}`, className].filter(Boolean).join(" ");
  return /*#__PURE__*/React.createElement("span", _extends({
    className: cls
  }, rest), dot && /*#__PURE__*/React.createElement("span", {
    className: "vela-badge__dot"
  }), children);
}
Object.assign(__ds_scope, { Badge });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/core/Badge.jsx", error: String((e && e.message) || e) }); }

// components/core/Card.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-card{
  background:var(--surface-card); border:1px solid var(--border-subtle);
  border-radius:var(--radius-lg); transition:var(--transition-colors), transform var(--dur-base) var(--ease-out);
}
.vela-card--pad-sm{ padding:var(--space-4); }
.vela-card--pad-md{ padding:var(--space-6); }
.vela-card--pad-lg{ padding:var(--space-8); }
.vela-card--flat{ box-shadow:none; }
.vela-card--raised{ box-shadow:var(--shadow-md); }
.vela-card--glow{ box-shadow:var(--shadow-md); border-color:var(--accent-subtle-border); }
.vela-card--interactive{ cursor:pointer; }
.vela-card--interactive:hover{ transform:translateY(-2px); box-shadow:var(--shadow-lg); border-color:var(--border-default); }
.vela-card--interactive:active{ transform:translateY(0); }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-card-css")) return;
  const el = document.createElement("style");
  el.id = "vela-card-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Card({
  elevation = "flat",
  padding = "md",
  interactive = false,
  as = "div",
  className = "",
  children,
  ...rest
}) {
  ensure();
  const Tag = as;
  const cls = ["vela-card", `vela-card--${elevation}`, `vela-card--pad-${padding}`, interactive ? "vela-card--interactive" : "", className].filter(Boolean).join(" ");
  return /*#__PURE__*/React.createElement(Tag, _extends({
    className: cls
  }, rest), children);
}
Object.assign(__ds_scope, { Card });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/core/Card.jsx", error: String((e && e.message) || e) }); }

// components/core/Spinner.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-spinner{ display:inline-block; border-radius:50%; border-style:solid; border-color:var(--border-default); border-right-color:var(--accent); animation:vela-spin .62s linear infinite; }
.vela-spinner--sm{ width:16px; height:16px; border-width:2px; }
.vela-spinner--md{ width:24px; height:24px; border-width:2.5px; }
.vela-spinner--lg{ width:36px; height:36px; border-width:3px; }
@keyframes vela-spin{ to{ transform:rotate(360deg); } }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-spinner-css")) return;
  const el = document.createElement("style");
  el.id = "vela-spinner-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Spinner({
  size = "md",
  className = "",
  label = "Loading",
  ...rest
}) {
  ensure();
  return /*#__PURE__*/React.createElement("span", _extends({
    role: "status",
    "aria-label": label,
    className: ["vela-spinner", `vela-spinner--${size}`, className].filter(Boolean).join(" ")
  }, rest));
}
Object.assign(__ds_scope, { Spinner });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/core/Spinner.jsx", error: String((e && e.message) || e) }); }

// components/core/Tabs.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-tabs{ display:flex; gap:var(--space-1); padding:var(--space-1); background:var(--surface-sunken); border-radius:var(--radius-md); }
.vela-tabs--line{ gap:var(--space-5); padding:0; background:none; border-bottom:1px solid var(--border-subtle); border-radius:0; }
.vela-tab{
  appearance:none; border:none; background:none; cursor:pointer;
  font-family:var(--font-body); font-size:var(--text-sm); font-weight:var(--weight-medium);
  color:var(--text-muted); padding:var(--space-2) var(--space-3); border-radius:var(--radius-sm);
  transition:var(--transition-colors); white-space:nowrap;
}
.vela-tab:hover{ color:var(--text-strong); }
.vela-tabs--pill .vela-tab[aria-selected="true"]{ background:var(--surface-card); color:var(--text-strong); box-shadow:var(--shadow-xs); }
.vela-tabs--line .vela-tab{ border-radius:0; padding:var(--space-3) 0; position:relative; }
.vela-tabs--line .vela-tab[aria-selected="true"]{ color:var(--accent-text); }
.vela-tabs--line .vela-tab[aria-selected="true"]::after{
  content:""; position:absolute; left:0; right:0; bottom:-1px; height:2px;
  background:var(--accent); border-radius:2px;
}
.vela-tab:focus-visible{ outline:2px solid var(--accent); outline-offset:2px; }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-tabs-css")) return;
  const el = document.createElement("style");
  el.id = "vela-tabs-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Tabs({
  items = [],
  value,
  defaultValue,
  onChange,
  variant = "pill",
  className = "",
  ...rest
}) {
  ensure();
  const norm = items.map(it => typeof it === "string" ? {
    value: it,
    label: it
  } : it);
  const [internal, setInternal] = React.useState(defaultValue ?? norm[0]?.value);
  const active = value !== undefined ? value : internal;
  const select = v => {
    if (value === undefined) setInternal(v);
    onChange && onChange(v);
  };
  return /*#__PURE__*/React.createElement("div", _extends({
    className: ["vela-tabs", `vela-tabs--${variant}`, className].filter(Boolean).join(" "),
    role: "tablist"
  }, rest), norm.map(it => /*#__PURE__*/React.createElement("button", {
    key: it.value,
    role: "tab",
    type: "button",
    "aria-selected": active === it.value,
    className: "vela-tab",
    onClick: () => select(it.value)
  }, it.label)));
}
Object.assign(__ds_scope, { Tabs });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/core/Tabs.jsx", error: String((e && e.message) || e) }); }

// components/forms/Button.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-btn{
  --_h: var(--control-md);
  --_px: var(--space-5);
  --_fs: var(--text-sm);
  display:inline-flex; align-items:center; justify-content:center; gap:var(--space-2);
  height:var(--_h); padding:0 var(--_px);
  font-family:var(--font-body); font-size:var(--_fs); font-weight:var(--weight-semibold);
  line-height:1; letter-spacing:var(--tracking-normal); white-space:nowrap;
  border:1px solid transparent; border-radius:var(--radius-md); cursor:pointer;
  transition:var(--transition-colors), transform var(--dur-fast) var(--ease-out);
  user-select:none; text-decoration:none;
}
.vela-btn:focus-visible{ outline:2px solid var(--accent); outline-offset:2px; }
.vela-btn:active{ transform:translateY(1px) scale(0.99); }
.vela-btn[disabled],.vela-btn[aria-disabled="true"]{ opacity:.5; cursor:not-allowed; transform:none; }
.vela-btn--sm{ --_h:var(--control-sm); --_px:var(--space-3); --_fs:var(--text-xs); }
.vela-btn--lg{ --_h:var(--control-lg); --_px:var(--space-6); --_fs:var(--text-base); }
.vela-btn--full{ width:100%; }

.vela-btn--primary{ background:var(--accent); color:var(--text-on-accent); }
.vela-btn--primary:hover{ background:var(--accent-hover); box-shadow:var(--glow-sm); }
.vela-btn--primary:active{ background:var(--accent-pressed); }

.vela-btn--secondary{ background:var(--surface-sunken); color:var(--text-strong); border-color:var(--border-subtle); }
.vela-btn--secondary:hover{ background:var(--surface-card); border-color:var(--border-default); }

.vela-btn--outline{ background:transparent; color:var(--accent-text); border-color:var(--accent-subtle-border); }
.vela-btn--outline:hover{ background:var(--accent-subtle); }

.vela-btn--ghost{ background:transparent; color:var(--text-body); }
.vela-btn--ghost:hover{ background:var(--surface-sunken); color:var(--text-strong); }

.vela-btn--danger{ background:var(--danger); color:#fff; }
.vela-btn--danger:hover{ filter:brightness(1.06); box-shadow:0 6px 18px color-mix(in oklch, var(--danger) 35%, transparent); }

.vela-btn__spin{ width:1em; height:1em; border:2px solid currentColor; border-right-color:transparent; border-radius:50%; animation:vela-btn-spin .6s linear infinite; }
@keyframes vela-btn-spin{ to{ transform:rotate(360deg); } }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-button-css")) return;
  const el = document.createElement("style");
  el.id = "vela-button-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Button({
  variant = "primary",
  size = "md",
  fullWidth = false,
  loading = false,
  disabled = false,
  iconLeft = null,
  iconRight = null,
  as = "button",
  className = "",
  children,
  ...rest
}) {
  ensure();
  const Tag = as;
  const cls = ["vela-btn", `vela-btn--${variant}`, size !== "md" ? `vela-btn--${size}` : "", fullWidth ? "vela-btn--full" : "", className].filter(Boolean).join(" ");
  const isDisabled = disabled || loading;
  const tagProps = Tag === "button" ? {
    type: rest.type || "button",
    disabled: isDisabled
  } : {
    "aria-disabled": isDisabled || undefined
  };
  return /*#__PURE__*/React.createElement(Tag, _extends({
    className: cls
  }, tagProps, rest), loading && /*#__PURE__*/React.createElement("span", {
    className: "vela-btn__spin",
    "aria-hidden": "true"
  }), !loading && iconLeft, children != null && /*#__PURE__*/React.createElement("span", null, children), !loading && iconRight);
}
Object.assign(__ds_scope, { Button });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/forms/Button.jsx", error: String((e && e.message) || e) }); }

// components/forms/Checkbox.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-check{ display:inline-flex; align-items:flex-start; gap:var(--space-2-5, 10px); cursor:pointer; user-select:none; }
.vela-check--disabled{ opacity:.5; cursor:not-allowed; }
.vela-check input{ position:absolute; opacity:0; width:0; height:0; }
.vela-check__box{
  flex:none; width:18px; height:18px; margin-top:1px; border-radius:var(--radius-xs);
  border:1.5px solid var(--border-strong); background:var(--surface-card);
  display:grid; place-items:center; transition:var(--transition-colors);
}
.vela-check__box svg{ width:12px; height:12px; stroke:#fff; stroke-width:3; fill:none; opacity:0; transition:opacity var(--dur-fast) var(--ease-out); }
.vela-check input:checked + .vela-check__box{ background:var(--accent); border-color:var(--accent); }
.vela-check input:checked + .vela-check__box svg{ opacity:1; }
.vela-check input:focus-visible + .vela-check__box{ box-shadow:0 0 0 3px var(--focus-ring); }
.vela-check__label{ font-size:var(--text-sm); color:var(--text-strong); line-height:var(--leading-snug); }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-check-css")) return;
  const el = document.createElement("style");
  el.id = "vela-check-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Checkbox({
  checked,
  defaultChecked,
  onChange,
  label,
  disabled = false,
  id,
  ...rest
}) {
  ensure();
  const fieldId = id || React.useId();
  return /*#__PURE__*/React.createElement("label", {
    className: ["vela-check", disabled ? "vela-check--disabled" : ""].filter(Boolean).join(" "),
    htmlFor: fieldId
  }, /*#__PURE__*/React.createElement("input", _extends({
    id: fieldId,
    type: "checkbox",
    checked: checked,
    defaultChecked: defaultChecked,
    onChange: onChange,
    disabled: disabled
  }, rest)), /*#__PURE__*/React.createElement("span", {
    className: "vela-check__box",
    "aria-hidden": "true"
  }, /*#__PURE__*/React.createElement("svg", {
    viewBox: "0 0 24 24"
  }, /*#__PURE__*/React.createElement("polyline", {
    points: "20 6 9 17 4 12"
  }))), label && /*#__PURE__*/React.createElement("span", {
    className: "vela-check__label"
  }, label));
}
Object.assign(__ds_scope, { Checkbox });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/forms/Checkbox.jsx", error: String((e && e.message) || e) }); }

// components/forms/Input.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-field{ display:flex; flex-direction:column; gap:var(--space-2); }
.vela-field__label{ font-size:var(--text-sm); font-weight:var(--weight-semibold); color:var(--text-strong); }
.vela-field__label .vela-req{ color:var(--danger); margin-left:2px; }
.vela-field__wrap{
  display:flex; align-items:center; gap:var(--space-2);
  height:var(--control-md); padding:0 var(--space-3);
  background:var(--surface-card); border:1px solid var(--border-default);
  border-radius:var(--radius-md); transition:var(--transition-colors);
}
.vela-field__wrap:hover{ border-color:var(--border-strong); }
.vela-field__wrap:focus-within{ border-color:var(--accent); box-shadow:0 0 0 3px var(--focus-ring); }
.vela-field__wrap--sm{ height:var(--control-sm); }
.vela-field__wrap--lg{ height:var(--control-lg); }
.vela-field__wrap--error{ border-color:var(--danger); }
.vela-field__wrap--error:focus-within{ box-shadow:0 0 0 3px color-mix(in oklch, var(--danger) 30%, transparent); }
.vela-field__wrap--disabled{ background:var(--surface-sunken); opacity:.6; cursor:not-allowed; }
.vela-field__input{
  flex:1; min-width:0; border:none; background:transparent; outline:none;
  font-family:var(--font-body); font-size:var(--text-sm); color:var(--text-strong);
}
.vela-field__input::placeholder{ color:var(--text-faint); }
.vela-field__addon{ display:flex; color:var(--text-muted); flex:none; }
.vela-field__hint{ font-size:var(--text-xs); color:var(--text-muted); }
.vela-field__hint--error{ color:var(--danger-text); }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-input-css")) return;
  const el = document.createElement("style");
  el.id = "vela-input-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Input({
  label,
  hint,
  error,
  required = false,
  size = "md",
  disabled = false,
  iconLeft = null,
  iconRight = null,
  id,
  className = "",
  ...rest
}) {
  ensure();
  const fieldId = id || React.useId();
  const wrapCls = ["vela-field__wrap", size !== "md" ? `vela-field__wrap--${size}` : "", error ? "vela-field__wrap--error" : "", disabled ? "vela-field__wrap--disabled" : ""].filter(Boolean).join(" ");
  return /*#__PURE__*/React.createElement("div", {
    className: ["vela-field", className].filter(Boolean).join(" ")
  }, label && /*#__PURE__*/React.createElement("label", {
    className: "vela-field__label",
    htmlFor: fieldId
  }, label, required && /*#__PURE__*/React.createElement("span", {
    className: "vela-req"
  }, "*")), /*#__PURE__*/React.createElement("div", {
    className: wrapCls
  }, iconLeft && /*#__PURE__*/React.createElement("span", {
    className: "vela-field__addon"
  }, iconLeft), /*#__PURE__*/React.createElement("input", _extends({
    id: fieldId,
    className: "vela-field__input",
    disabled: disabled,
    "aria-invalid": error ? "true" : undefined
  }, rest)), iconRight && /*#__PURE__*/React.createElement("span", {
    className: "vela-field__addon"
  }, iconRight)), (error || hint) && /*#__PURE__*/React.createElement("span", {
    className: ["vela-field__hint", error ? "vela-field__hint--error" : ""].filter(Boolean).join(" ")
  }, error || hint));
}
Object.assign(__ds_scope, { Input });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/forms/Input.jsx", error: String((e && e.message) || e) }); }

// components/forms/Select.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-select{ display:flex; flex-direction:column; gap:var(--space-2); }
.vela-select__label{ font-size:var(--text-sm); font-weight:var(--weight-semibold); color:var(--text-strong); }
.vela-select__wrap{ position:relative; display:flex; align-items:center; }
.vela-select__el{
  appearance:none; width:100%; height:var(--control-md);
  padding:0 var(--space-9, 36px) 0 var(--space-3);
  background:var(--surface-card); color:var(--text-strong);
  border:1px solid var(--border-default); border-radius:var(--radius-md);
  font-family:var(--font-body); font-size:var(--text-sm); cursor:pointer;
  transition:var(--transition-colors);
}
.vela-select__el:hover{ border-color:var(--border-strong); }
.vela-select__el:focus{ outline:none; border-color:var(--accent); box-shadow:0 0 0 3px var(--focus-ring); }
.vela-select__el:disabled{ background:var(--surface-sunken); opacity:.6; cursor:not-allowed; }
.vela-select__el--sm{ height:var(--control-sm); font-size:var(--text-xs); }
.vela-select__el--lg{ height:var(--control-lg); font-size:var(--text-base); }
.vela-select__chev{ position:absolute; right:var(--space-3); pointer-events:none; color:var(--text-muted); display:flex; }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-select-css")) return;
  const el = document.createElement("style");
  el.id = "vela-select-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Select({
  label,
  options = [],
  size = "md",
  placeholder,
  id,
  className = "",
  children,
  ...rest
}) {
  ensure();
  const fieldId = id || React.useId();
  return /*#__PURE__*/React.createElement("div", {
    className: ["vela-select", className].filter(Boolean).join(" ")
  }, label && /*#__PURE__*/React.createElement("label", {
    className: "vela-select__label",
    htmlFor: fieldId
  }, label), /*#__PURE__*/React.createElement("div", {
    className: "vela-select__wrap"
  }, /*#__PURE__*/React.createElement("select", _extends({
    id: fieldId,
    className: ["vela-select__el", size !== "md" ? `vela-select__el--${size}` : ""].filter(Boolean).join(" ")
  }, rest), placeholder && /*#__PURE__*/React.createElement("option", {
    value: "",
    disabled: true
  }, placeholder), options.map(o => {
    const opt = typeof o === "string" ? {
      value: o,
      label: o
    } : o;
    return /*#__PURE__*/React.createElement("option", {
      key: opt.value,
      value: opt.value
    }, opt.label);
  }), children), /*#__PURE__*/React.createElement("span", {
    className: "vela-select__chev",
    "aria-hidden": "true"
  }, /*#__PURE__*/React.createElement("svg", {
    width: "16",
    height: "16",
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: "2.2",
    strokeLinecap: "round",
    strokeLinejoin: "round"
  }, /*#__PURE__*/React.createElement("polyline", {
    points: "6 9 12 15 18 9"
  })))));
}
Object.assign(__ds_scope, { Select });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/forms/Select.jsx", error: String((e && e.message) || e) }); }

// components/forms/Switch.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.vela-switch{ display:inline-flex; align-items:center; gap:var(--space-3); cursor:pointer; user-select:none; }
.vela-switch--disabled{ opacity:.5; cursor:not-allowed; }
.vela-switch__track{
  position:relative; flex:none; width:40px; height:24px; border-radius:var(--radius-full);
  background:var(--border-default); transition:background var(--dur-base) var(--ease-out);
}
.vela-switch__track--lg{ width:48px; height:28px; }
.vela-switch__thumb{
  position:absolute; top:3px; left:3px; width:18px; height:18px; border-radius:50%;
  background:#fff; box-shadow:var(--shadow-sm);
  transition:transform var(--dur-base) var(--ease-spring);
}
.vela-switch__track--lg .vela-switch__thumb{ width:22px; height:22px; }
.vela-switch input{ position:absolute; opacity:0; width:0; height:0; }
.vela-switch input:checked + .vela-switch__track{ background:var(--accent); }
.vela-switch input:checked + .vela-switch__track .vela-switch__thumb{ transform:translateX(16px); }
.vela-switch input:checked + .vela-switch__track--lg .vela-switch__thumb{ transform:translateX(20px); }
.vela-switch input:focus-visible + .vela-switch__track{ box-shadow:0 0 0 3px var(--focus-ring); }
.vela-switch__label{ font-size:var(--text-sm); color:var(--text-strong); font-weight:var(--weight-medium); }
`;
function ensure() {
  if (typeof document === "undefined" || document.getElementById("vela-switch-css")) return;
  const el = document.createElement("style");
  el.id = "vela-switch-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}
function Switch({
  checked,
  defaultChecked,
  onChange,
  label,
  size = "md",
  disabled = false,
  id,
  ...rest
}) {
  ensure();
  const fieldId = id || React.useId();
  return /*#__PURE__*/React.createElement("label", {
    className: ["vela-switch", disabled ? "vela-switch--disabled" : ""].filter(Boolean).join(" "),
    htmlFor: fieldId
  }, /*#__PURE__*/React.createElement("input", _extends({
    id: fieldId,
    type: "checkbox",
    role: "switch",
    checked: checked,
    defaultChecked: defaultChecked,
    onChange: onChange,
    disabled: disabled
  }, rest)), /*#__PURE__*/React.createElement("span", {
    className: ["vela-switch__track", size === "lg" ? "vela-switch__track--lg" : ""].filter(Boolean).join(" ")
  }, /*#__PURE__*/React.createElement("span", {
    className: "vela-switch__thumb"
  })), label && /*#__PURE__*/React.createElement("span", {
    className: "vela-switch__label"
  }, label));
}
Object.assign(__ds_scope, { Switch });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/forms/Switch.jsx", error: String((e && e.message) || e) }); }

// ui_kits/mobile/MobileApp.jsx
try { (() => {
// Vela Mobile — full app (screens + tab bar) in a phone frame
function MIcon({
  name,
  size = 22,
  color,
  style
}) {
  return /*#__PURE__*/React.createElement("i", {
    "data-lucide": name,
    width: size,
    height: size,
    style: {
      display: "inline-flex",
      width: size,
      height: size,
      color,
      ...style
    }
  });
}
function relucide() {
  if (window.lucide) window.lucide.createIcons();
}
function StatusBar() {
  return /*#__PURE__*/React.createElement("div", {
    style: mb.status
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      fontWeight: 700
    }
  }, "9:41"), /*#__PURE__*/React.createElement("span", {
    style: {
      display: "flex",
      gap: 6,
      alignItems: "center"
    }
  }, /*#__PURE__*/React.createElement(MIcon, {
    name: "signal",
    size: 15
  }), /*#__PURE__*/React.createElement(MIcon, {
    name: "wifi",
    size: 15
  }), /*#__PURE__*/React.createElement(MIcon, {
    name: "battery-full",
    size: 17
  })));
}
function HomeScreen() {
  const {
    Card,
    Badge
  } = window.VelaDesignSystem_c1e10b;
  React.useEffect(relucide);
  const metrics = [{
    l: "请求量",
    v: "1.24M",
    d: "+12%",
    c: "var(--azure-500)"
  }, {
    l: "连接数",
    v: "3.9K",
    d: "+8%",
    c: "var(--cyan-500)"
  }];
  const feed = [{
    who: "Lin Wei",
    what: "部署了 realtime-gateway",
    when: "2 分钟前",
    icon: "rocket"
  }, {
    who: "Ada Lou",
    what: "邀请了 3 位成员",
    when: "1 小时前",
    icon: "user-plus"
  }, {
    who: "系统",
    what: "用量达到配额 60%",
    when: "3 小时前",
    icon: "alert-triangle"
  }];
  return /*#__PURE__*/React.createElement("div", {
    style: mb.screen
  }, /*#__PURE__*/React.createElement("div", {
    style: mb.header
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 13,
      color: "var(--text-muted)"
    }
  }, "\u665A\u4E0A\u597D \uD83D\uDC4B"), /*#__PURE__*/React.createElement("div", {
    style: {
      fontFamily: "var(--font-display)",
      fontSize: 24,
      fontWeight: 700,
      color: "var(--text-strong)",
      letterSpacing: "-0.02em"
    }
  }, "Acme Inc.")), /*#__PURE__*/React.createElement("div", {
    style: mb.avatar
  }, "LW")), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1fr 1fr",
      gap: 12
    }
  }, metrics.map(m => /*#__PURE__*/React.createElement(Card, {
    key: m.l,
    elevation: "raised",
    padding: "md"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 12,
      color: "var(--text-muted)"
    }
  }, m.l), /*#__PURE__*/React.createElement("div", {
    style: {
      fontFamily: "var(--font-display)",
      fontSize: 26,
      fontWeight: 700,
      color: "var(--text-strong)",
      margin: "4px 0 2px"
    }
  }, m.v), /*#__PURE__*/React.createElement(Badge, {
    color: "success"
  }, m.d)))), /*#__PURE__*/React.createElement(Card, {
    elevation: "glow",
    padding: "md"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 12
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: mb.upIcon
  }, /*#__PURE__*/React.createElement(MIcon, {
    name: "zap",
    size: 20,
    color: "#fff"
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontWeight: 700,
      color: "var(--text-strong)"
    }
  }, "\u5347\u7EA7\u5230 Team"), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 12.5,
      color: "var(--text-muted)"
    }
  }, "\u89E3\u9501\u65E0\u9650\u9879\u76EE\u4E0E\u534F\u4F5C")), /*#__PURE__*/React.createElement(MIcon, {
    name: "chevron-right",
    size: 20,
    color: "var(--text-muted)"
  }))), /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: mb.sectionTitle
  }, "\u6700\u8FD1\u52A8\u6001"), /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "sm"
  }, feed.map((a, i) => /*#__PURE__*/React.createElement("div", {
    key: i,
    style: {
      display: "flex",
      gap: 12,
      alignItems: "center",
      padding: "11px 6px",
      borderBottom: i < feed.length - 1 ? "1px solid var(--border-subtle)" : "none"
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: mb.feedIcon
  }, /*#__PURE__*/React.createElement(MIcon, {
    name: a.icon,
    size: 16,
    color: "var(--accent-text)"
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1,
      minWidth: 0
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 13.5,
      color: "var(--text-strong)"
    }
  }, /*#__PURE__*/React.createElement("b", {
    style: {
      fontWeight: 600
    }
  }, a.who), " ", a.what), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 11.5,
      color: "var(--text-faint)"
    }
  }, a.when)))))));
}
function ProjectsScreenM() {
  const {
    Card,
    Badge
  } = window.VelaDesignSystem_c1e10b;
  React.useEffect(relucide);
  const rows = [{
    name: "Atlas Analytics",
    status: "运行中",
    tone: "success",
    reqs: "428K"
  }, {
    name: "Realtime Gateway",
    status: "运行中",
    tone: "success",
    reqs: "1.1M"
  }, {
    name: "Nova Search",
    status: "部署中",
    tone: "accent",
    reqs: "62K"
  }, {
    name: "Pulse Notifications",
    status: "降级",
    tone: "warning",
    reqs: "240K"
  }, {
    name: "Ledger Sync",
    status: "已暂停",
    tone: "neutral",
    reqs: "—"
  }];
  return /*#__PURE__*/React.createElement("div", {
    style: mb.screen
  }, /*#__PURE__*/React.createElement("div", {
    style: mb.header
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontFamily: "var(--font-display)",
      fontSize: 24,
      fontWeight: 700,
      color: "var(--text-strong)"
    }
  }, "\u9879\u76EE"), /*#__PURE__*/React.createElement("div", {
    style: mb.iconBtn
  }, /*#__PURE__*/React.createElement(MIcon, {
    name: "plus",
    size: 20,
    color: "var(--accent-text)"
  }))), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column",
      gap: 10
    }
  }, rows.map(r => /*#__PURE__*/React.createElement(Card, {
    key: r.name,
    elevation: "raised",
    padding: "md",
    interactive: true
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 12
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: mb.projIcon
  }, /*#__PURE__*/React.createElement(MIcon, {
    name: "box",
    size: 18,
    color: "var(--accent-text)"
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontWeight: 600,
      color: "var(--text-strong)",
      fontSize: 15
    }
  }, r.name), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 12.5,
      color: "var(--text-muted)",
      fontFamily: "var(--font-mono)"
    }
  }, r.reqs, " \u8BF7\u6C42 / \u4ECA\u65E5")), /*#__PURE__*/React.createElement(Badge, {
    color: r.tone,
    dot: true
  }, r.status))))));
}
function SettingsScreenM({
  theme,
  onToggle
}) {
  const {
    Card,
    Switch,
    Avatar
  } = window.VelaDesignSystem_c1e10b;
  React.useEffect(relucide);
  return /*#__PURE__*/React.createElement("div", {
    style: mb.screen
  }, /*#__PURE__*/React.createElement("div", {
    style: mb.header
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontFamily: "var(--font-display)",
      fontSize: 24,
      fontWeight: 700,
      color: "var(--text-strong)"
    }
  }, "\u8BBE\u7F6E")), /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "md"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 14
    }
  }, /*#__PURE__*/React.createElement(Avatar, {
    name: "Lin Wei",
    size: "lg",
    status: "online"
  }), /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: {
      fontWeight: 700,
      color: "var(--text-strong)",
      fontSize: 16
    }
  }, "Lin Wei"), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 13,
      color: "var(--text-muted)"
    }
  }, "lin.wei@acme.com")))), /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "sm"
  }, [["暗色模式", "moon", true], ["推送通知", "bell", false], ["双因素认证", "shield-check", false]].map(([l, ic, isTheme], i) => /*#__PURE__*/React.createElement("div", {
    key: l,
    style: {
      display: "flex",
      alignItems: "center",
      gap: 12,
      padding: "13px 8px",
      borderBottom: i < 2 ? "1px solid var(--border-subtle)" : "none"
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: mb.feedIcon
  }, /*#__PURE__*/React.createElement(MIcon, {
    name: ic,
    size: 16,
    color: "var(--text-body)"
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1,
      fontWeight: 500,
      color: "var(--text-strong)",
      fontSize: 14
    }
  }, l), isTheme ? /*#__PURE__*/React.createElement(Switch, {
    checked: theme === "dark",
    onChange: onToggle
  }) : /*#__PURE__*/React.createElement(Switch, {
    defaultChecked: i === 1
  })))));
}
function TabBar({
  tab,
  onTab
}) {
  React.useEffect(relucide);
  const tabs = [["home", "首页", "home"], ["projects", "项目", "folder-kanban"], ["settings", "我的", "user"]];
  return /*#__PURE__*/React.createElement("div", {
    style: mb.tabbar
  }, tabs.map(([id, label, icon]) => {
    const on = tab === id;
    return /*#__PURE__*/React.createElement("button", {
      key: id,
      onClick: () => onTab(id),
      style: {
        ...mb.tab,
        color: on ? "var(--accent-text)" : "var(--text-faint)"
      }
    }, /*#__PURE__*/React.createElement(MIcon, {
      name: icon,
      size: 22
    }), /*#__PURE__*/React.createElement("span", {
      style: {
        fontSize: 10.5,
        fontWeight: on ? 600 : 500
      }
    }, label));
  }));
}
function MobileApp() {
  const [tab, setTab] = React.useState("home");
  const [theme, setTheme] = React.useState("light");
  React.useEffect(() => {
    const s = localStorage.getItem("vela-mobile-theme");
    if (s) setTheme(s);
  }, []);
  const toggle = () => setTheme(t => t === "dark" ? "light" : "dark");
  React.useEffect(() => {
    localStorage.setItem("vela-mobile-theme", theme);
    relucide();
  }, [theme, tab]);
  const screen = {
    home: /*#__PURE__*/React.createElement(HomeScreen, null),
    projects: /*#__PURE__*/React.createElement(ProjectsScreenM, null),
    settings: /*#__PURE__*/React.createElement(SettingsScreenM, {
      theme: theme,
      onToggle: toggle
    })
  }[tab];
  return /*#__PURE__*/React.createElement("div", {
    style: mb.stage
  }, /*#__PURE__*/React.createElement("div", {
    style: mb.phone
  }, /*#__PURE__*/React.createElement("div", {
    style: mb.notch
  }), /*#__PURE__*/React.createElement("div", {
    "data-theme": theme,
    style: mb.viewport
  }, /*#__PURE__*/React.createElement(StatusBar, null), /*#__PURE__*/React.createElement("div", {
    style: mb.content
  }, screen), /*#__PURE__*/React.createElement(TabBar, {
    tab: tab,
    onTab: setTab
  }))));
}
const mb = {
  stage: {
    minHeight: "100%",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    padding: 30,
    background: "var(--surface-page)"
  },
  phone: {
    position: "relative",
    width: 390,
    height: 800,
    background: "#0a0c14",
    borderRadius: 52,
    padding: 12,
    boxShadow: "0 40px 90px rgba(16,24,48,.35), 0 0 0 2px rgba(0,0,0,.2)"
  },
  notch: {
    position: "absolute",
    top: 22,
    left: "50%",
    transform: "translateX(-50%)",
    width: 120,
    height: 30,
    background: "#0a0c14",
    borderRadius: 18,
    zIndex: 10
  },
  viewport: {
    position: "relative",
    width: "100%",
    height: "100%",
    background: "var(--surface-page)",
    borderRadius: 40,
    overflow: "hidden",
    display: "flex",
    flexDirection: "column"
  },
  status: {
    height: 50,
    padding: "0 28px",
    display: "flex",
    alignItems: "flex-end",
    justifyContent: "space-between",
    paddingBottom: 6,
    fontSize: 14,
    color: "var(--text-strong)",
    flex: "none"
  },
  content: {
    flex: 1,
    overflow: "auto"
  },
  screen: {
    padding: "8px 18px 18px",
    display: "flex",
    flexDirection: "column",
    gap: 16
  },
  header: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    paddingTop: 8
  },
  avatar: {
    width: 42,
    height: 42,
    borderRadius: "50%",
    background: "var(--accent-subtle)",
    color: "var(--accent-text)",
    display: "grid",
    placeItems: "center",
    fontWeight: 700,
    fontSize: 14
  },
  iconBtn: {
    width: 40,
    height: 40,
    borderRadius: "var(--radius-md)",
    background: "var(--accent-subtle)",
    display: "grid",
    placeItems: "center",
    border: "none",
    cursor: "pointer"
  },
  upIcon: {
    width: 42,
    height: 42,
    flex: "none",
    borderRadius: "var(--radius-md)",
    background: "linear-gradient(135deg, var(--azure-500), var(--cyan-400))",
    display: "grid",
    placeItems: "center"
  },
  sectionTitle: {
    fontSize: 13,
    fontWeight: 600,
    color: "var(--text-muted)",
    marginBottom: 10
  },
  feedIcon: {
    width: 30,
    height: 30,
    flex: "none",
    borderRadius: "var(--radius-md)",
    background: "var(--surface-sunken)",
    display: "grid",
    placeItems: "center"
  },
  projIcon: {
    width: 38,
    height: 38,
    flex: "none",
    borderRadius: "var(--radius-md)",
    background: "var(--accent-subtle)",
    display: "grid",
    placeItems: "center"
  },
  tabbar: {
    flex: "none",
    height: 78,
    display: "flex",
    borderTop: "1px solid var(--border-subtle)",
    background: "var(--surface-card)",
    paddingBottom: 14
  },
  tab: {
    flex: 1,
    border: "none",
    background: "none",
    cursor: "pointer",
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    gap: 4,
    paddingTop: 10
  }
};
Object.assign(window, {
  MobileApp
});
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/mobile/MobileApp.jsx", error: String((e && e.message) || e) }); }

// ui_kits/web/App.jsx
try { (() => {
// Vela Web — App shell + router
function MembersScreen() {
  const {
    Card,
    Avatar,
    Badge,
    Button,
    Select
  } = window.VelaDesignSystem_c1e10b;
  useLucide();
  const people = [{
    n: "Lin Wei",
    e: "lin.wei@acme.com",
    role: "Owner",
    status: "online"
  }, {
    n: "Ada Lou",
    e: "ada@acme.com",
    role: "Admin",
    status: "online"
  }, {
    n: "Chen Hao",
    e: "hao.chen@acme.com",
    role: "Developer",
    status: "away"
  }, {
    n: "Mei Zhang",
    e: "mei.z@acme.com",
    role: "Developer",
    status: "offline"
  }, {
    n: "Sam Okoro",
    e: "sam@acme.com",
    role: "Viewer",
    status: "busy"
  }];
  return /*#__PURE__*/React.createElement("div", {
    style: {
      padding: 28,
      display: "flex",
      flexDirection: "column",
      gap: 18
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      color: "var(--text-muted)",
      fontSize: 14
    }
  }, "\u56E2\u961F\u5171 ", people.length, " \u4F4D\u6210\u5458 \xB7 \u8FD8\u53EF\u9080\u8BF7 15 \u4F4D"), /*#__PURE__*/React.createElement("div", {
    style: {
      marginLeft: "auto"
    }
  }, /*#__PURE__*/React.createElement(Button, {
    iconLeft: /*#__PURE__*/React.createElement(Icon, {
      name: "user-plus",
      size: 16
    })
  }, "\u9080\u8BF7\u6210\u5458"))), /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "sm"
  }, people.map((p, i) => /*#__PURE__*/React.createElement("div", {
    key: p.e,
    style: {
      display: "flex",
      alignItems: "center",
      gap: 14,
      padding: "13px 14px",
      borderBottom: i < people.length - 1 ? "1px solid var(--border-subtle)" : "none"
    }
  }, /*#__PURE__*/React.createElement(Avatar, {
    name: p.n,
    status: p.status
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontWeight: 600,
      color: "var(--text-strong)",
      fontSize: 14
    }
  }, p.n), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 13,
      color: "var(--text-muted)"
    }
  }, p.e)), /*#__PURE__*/React.createElement(Badge, {
    color: p.role === "Owner" ? "accent" : "neutral"
  }, p.role), /*#__PURE__*/React.createElement("button", {
    style: {
      border: "none",
      background: "none",
      cursor: "pointer",
      color: "var(--text-muted)",
      marginLeft: 8
    }
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "more-horizontal",
    size: 18
  }))))));
}
function SettingsScreen({
  theme,
  onToggleTheme
}) {
  const {
    Card,
    Input,
    Switch,
    Select,
    Button
  } = window.VelaDesignSystem_c1e10b;
  useLucide();
  return /*#__PURE__*/React.createElement("div", {
    style: {
      padding: 28,
      maxWidth: 680,
      display: "flex",
      flexDirection: "column",
      gap: 16
    }
  }, /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "lg"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 16,
      fontWeight: 700,
      color: "var(--text-strong)",
      marginBottom: 16
    }
  }, "\u5DE5\u4F5C\u533A"), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1fr 1fr",
      gap: 14
    }
  }, /*#__PURE__*/React.createElement(Input, {
    label: "\u5DE5\u4F5C\u533A\u540D\u79F0",
    defaultValue: "Acme Inc."
  }), /*#__PURE__*/React.createElement(Select, {
    label: "\u9ED8\u8BA4\u5730\u533A",
    options: ["华东 (上海)", "华北 (北京)", "华南 (深圳)"]
  }))), /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "lg"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 16,
      fontWeight: 700,
      color: "var(--text-strong)",
      marginBottom: 16
    }
  }, "\u504F\u597D"), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column",
      gap: 16
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: st.row
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: st.t
  }, "\u6697\u8272\u6A21\u5F0F"), /*#__PURE__*/React.createElement("div", {
    style: st.d
  }, "\u8DDF\u968F\u5F53\u524D\u4E3B\u9898\u5207\u6362")), /*#__PURE__*/React.createElement(Switch, {
    checked: theme === "dark",
    onChange: onToggleTheme
  })), /*#__PURE__*/React.createElement("div", {
    style: st.row
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: st.t
  }, "\u90AE\u4EF6\u901A\u77E5"), /*#__PURE__*/React.createElement("div", {
    style: st.d
  }, "\u90E8\u7F72\u3001\u914D\u989D\u4E0E\u5B89\u5168\u63D0\u9192")), /*#__PURE__*/React.createElement(Switch, {
    defaultChecked: true
  })), /*#__PURE__*/React.createElement("div", {
    style: st.row
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: st.t
  }, "\u53CC\u56E0\u7D20\u8BA4\u8BC1"), /*#__PURE__*/React.createElement("div", {
    style: st.d
  }, "\u767B\u5F55\u65F6\u8981\u6C42\u9A8C\u8BC1\u7801")), /*#__PURE__*/React.createElement(Switch, {
    defaultChecked: true
  })))), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      gap: 10
    }
  }, /*#__PURE__*/React.createElement(Button, null, "\u4FDD\u5B58\u66F4\u6539"), /*#__PURE__*/React.createElement(Button, {
    variant: "ghost"
  }, "\u53D6\u6D88")));
}
const st = {
  row: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between"
  },
  t: {
    fontWeight: 600,
    color: "var(--text-strong)",
    fontSize: 14
  },
  d: {
    fontSize: 13,
    color: "var(--text-muted)",
    marginTop: 2
  }
};
function RealtimeScreen() {
  const {
    Card,
    Badge
  } = window.VelaDesignSystem_c1e10b;
  const [n, setN] = React.useState(3912);
  React.useEffect(() => {
    const t = setInterval(() => setN(v => v + Math.round((Math.random() - 0.4) * 40)), 1500);
    return () => clearInterval(t);
  }, []);
  useLucide();
  const bars = [40, 62, 48, 80, 56, 92, 70, 88, 64, 96, 78, 84];
  return /*#__PURE__*/React.createElement("div", {
    style: {
      padding: 28,
      display: "flex",
      flexDirection: "column",
      gap: 18
    }
  }, /*#__PURE__*/React.createElement(Card, {
    elevation: "glow",
    padding: "lg"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 10
    }
  }, /*#__PURE__*/React.createElement(Badge, {
    color: "success",
    dot: true
  }, "LIVE"), /*#__PURE__*/React.createElement("span", {
    style: {
      color: "var(--text-muted)",
      fontSize: 14
    }
  }, "\u5F53\u524D\u6D3B\u8DC3\u5B9E\u65F6\u8FDE\u63A5")), /*#__PURE__*/React.createElement("div", {
    style: {
      fontFamily: "var(--font-display)",
      fontSize: 56,
      fontWeight: 700,
      color: "var(--text-strong)",
      letterSpacing: "-0.03em",
      margin: "6px 0 18px"
    }
  }, n.toLocaleString()), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "flex-end",
      gap: 8,
      height: 120
    }
  }, bars.map((b, i) => /*#__PURE__*/React.createElement("div", {
    key: i,
    style: {
      flex: 1,
      height: `${b}%`,
      background: i === bars.length - 1 ? "linear-gradient(180deg, var(--cyan-400), var(--azure-500))" : "var(--surface-sunken)",
      borderRadius: "var(--radius-sm)"
    }
  })))));
}
function App() {
  const [authed, setAuthed] = React.useState(false);
  const [nav, setNav] = React.useState("dashboard");
  const [theme, setTheme] = React.useState("light");
  React.useEffect(() => {
    const saved = localStorage.getItem("vela-web-theme");
    if (saved) setTheme(saved);
  }, []);
  React.useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
    localStorage.setItem("vela-web-theme", theme);
    if (window.lucide) window.lucide.createIcons();
  }, [theme]);
  const toggleTheme = () => setTheme(t => t === "dark" ? "light" : "dark");
  if (!authed) return /*#__PURE__*/React.createElement(LoginScreen, {
    onLogin: () => setAuthed(true)
  });
  const titles = {
    dashboard: "概览",
    projects: "项目",
    realtime: "实时监控",
    members: "成员",
    settings: "设置"
  };
  const screen = {
    dashboard: /*#__PURE__*/React.createElement(DashboardScreen, null),
    projects: /*#__PURE__*/React.createElement(ProjectsScreen, null),
    realtime: /*#__PURE__*/React.createElement(RealtimeScreen, null),
    members: /*#__PURE__*/React.createElement(MembersScreen, null),
    settings: /*#__PURE__*/React.createElement(SettingsScreen, {
      theme: theme,
      onToggleTheme: toggleTheme
    })
  }[nav];
  return /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      height: "100%",
      background: "var(--surface-page)"
    }
  }, /*#__PURE__*/React.createElement(Sidebar, {
    active: nav,
    onNavigate: setNav
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1,
      minWidth: 0,
      display: "flex",
      flexDirection: "column",
      overflow: "auto"
    }
  }, /*#__PURE__*/React.createElement(Topbar, {
    title: titles[nav],
    theme: theme,
    onToggleTheme: toggleTheme,
    onLogout: () => setAuthed(false)
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1
    }
  }, screen)));
}
Object.assign(window, {
  App,
  MembersScreen,
  SettingsScreen,
  RealtimeScreen
});
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/web/App.jsx", error: String((e && e.message) || e) }); }

// ui_kits/web/Chrome.jsx
try { (() => {
// Vela Web UI kit — shared primitives (Icon helper + layout chrome)
// Icons: Lucide (MIT) loaded from CDN via window.lucide.createIcons().

function Icon({
  name,
  size = 18,
  color,
  strokeWidth = 2,
  style
}) {
  return /*#__PURE__*/React.createElement("i", {
    "data-lucide": name,
    width: size,
    height: size,
    "stroke-width": strokeWidth,
    style: {
      display: "inline-flex",
      width: size,
      height: size,
      color,
      ...style
    }
  });
}

// Re-render Lucide whenever the DOM changes.
function useLucide(dep) {
  React.useEffect(() => {
    if (window.lucide) window.lucide.createIcons();
  });
}
const NAV = [{
  id: "dashboard",
  label: "概览 Overview",
  icon: "layout-dashboard"
}, {
  id: "projects",
  label: "项目 Projects",
  icon: "folder-kanban"
}, {
  id: "realtime",
  label: "实时 Realtime",
  icon: "activity"
}, {
  id: "members",
  label: "成员 Members",
  icon: "users"
}, {
  id: "settings",
  label: "设置 Settings",
  icon: "settings"
}];
function Sidebar({
  active,
  onNavigate
}) {
  return /*#__PURE__*/React.createElement("aside", {
    style: sb.root
  }, /*#__PURE__*/React.createElement("div", {
    style: sb.brand
  }, /*#__PURE__*/React.createElement("img", {
    src: "../../assets/vela-mark.svg",
    width: "30",
    height: "30",
    alt: "Vela"
  }), /*#__PURE__*/React.createElement("span", {
    style: sb.brandName
  }, "Vela"), /*#__PURE__*/React.createElement("span", {
    style: sb.plan
  }, "Pro")), /*#__PURE__*/React.createElement("nav", {
    style: sb.nav
  }, NAV.map(n => {
    const on = active === n.id;
    return /*#__PURE__*/React.createElement("button", {
      key: n.id,
      onClick: () => onNavigate(n.id),
      style: {
        ...sb.item,
        ...(on ? sb.itemActive : null)
      }
    }, /*#__PURE__*/React.createElement(Icon, {
      name: n.icon,
      size: 18
    }), /*#__PURE__*/React.createElement("span", null, n.label));
  })), /*#__PURE__*/React.createElement("div", {
    style: sb.bottom
  }, /*#__PURE__*/React.createElement("div", {
    style: sb.usage
  }, /*#__PURE__*/React.createElement("div", {
    style: sb.usageRow
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      color: "var(--text-muted)"
    }
  }, "\u672C\u6708\u7528\u91CF"), /*#__PURE__*/React.createElement("span", {
    style: {
      color: "var(--text-strong)",
      fontWeight: 600
    }
  }, "62%")), /*#__PURE__*/React.createElement("div", {
    style: sb.track
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      ...sb.fill,
      width: "62%"
    }
  })))));
}
const sb = {
  root: {
    width: 248,
    flex: "none",
    height: "100%",
    boxSizing: "border-box",
    padding: "20px 14px",
    background: "var(--surface-card)",
    borderRight: "1px solid var(--border-subtle)",
    display: "flex",
    flexDirection: "column",
    gap: 8
  },
  brand: {
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "4px 8px 16px"
  },
  brandName: {
    fontFamily: "var(--font-display)",
    fontWeight: 700,
    fontSize: 20,
    color: "var(--text-strong)",
    letterSpacing: "-0.02em"
  },
  plan: {
    marginLeft: "auto",
    fontSize: 11,
    fontWeight: 600,
    color: "var(--accent-text)",
    background: "var(--accent-subtle)",
    padding: "2px 8px",
    borderRadius: "var(--radius-full)"
  },
  nav: {
    display: "flex",
    flexDirection: "column",
    gap: 2
  },
  item: {
    display: "flex",
    alignItems: "center",
    gap: 11,
    padding: "9px 11px",
    border: "none",
    background: "none",
    borderRadius: "var(--radius-md)",
    cursor: "pointer",
    color: "var(--text-muted)",
    fontFamily: "var(--font-body)",
    fontSize: 14,
    fontWeight: 500,
    textAlign: "left",
    width: "100%",
    transition: "var(--transition-colors)"
  },
  itemActive: {
    background: "var(--accent-subtle)",
    color: "var(--accent-text)",
    fontWeight: 600
  },
  bottom: {
    marginTop: "auto"
  },
  usage: {
    padding: 12,
    background: "var(--surface-sunken)",
    borderRadius: "var(--radius-md)"
  },
  usageRow: {
    display: "flex",
    justifyContent: "space-between",
    fontSize: 12,
    marginBottom: 8
  },
  track: {
    height: 6,
    background: "var(--border-subtle)",
    borderRadius: "var(--radius-full)",
    overflow: "hidden"
  },
  fill: {
    height: "100%",
    background: "linear-gradient(90deg, var(--azure-500), var(--cyan-400))",
    borderRadius: "var(--radius-full)"
  }
};
function Topbar({
  title,
  theme,
  onToggleTheme,
  onLogout
}) {
  return /*#__PURE__*/React.createElement("header", {
    style: tb.root
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("h1", {
    style: tb.title
  }, title)), /*#__PURE__*/React.createElement("div", {
    style: tb.right
  }, /*#__PURE__*/React.createElement("div", {
    style: tb.search
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "search",
    size: 16,
    style: {
      color: "var(--text-muted)"
    }
  }), /*#__PURE__*/React.createElement("input", {
    style: tb.searchInput,
    placeholder: "\u641C\u7D22\u9879\u76EE\u3001\u6210\u5458\u2026"
  }), /*#__PURE__*/React.createElement("kbd", {
    style: tb.kbd
  }, "\u2318K")), /*#__PURE__*/React.createElement("button", {
    style: tb.iconBtn,
    onClick: onToggleTheme,
    title: "\u5207\u6362\u4E3B\u9898"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: theme === "dark" ? "sun" : "moon",
    size: 18
  })), /*#__PURE__*/React.createElement("button", {
    style: tb.iconBtn,
    title: "\u901A\u77E5"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "bell",
    size: 18
  }), /*#__PURE__*/React.createElement("span", {
    style: tb.dot
  })), /*#__PURE__*/React.createElement("button", {
    style: tb.avatar,
    onClick: onLogout,
    title: "\u9000\u51FA\u767B\u5F55"
  }, "LW")));
}
const tb = {
  root: {
    height: 64,
    flex: "none",
    boxSizing: "border-box",
    padding: "0 28px",
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    borderBottom: "1px solid var(--border-subtle)",
    background: "color-mix(in oklch, var(--surface-card) 80%, transparent)",
    backdropFilter: "blur(8px)",
    position: "sticky",
    top: 0,
    zIndex: 100
  },
  title: {
    fontSize: 20,
    fontWeight: 700,
    color: "var(--text-strong)"
  },
  right: {
    display: "flex",
    alignItems: "center",
    gap: 10
  },
  search: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    height: 38,
    padding: "0 12px",
    background: "var(--surface-sunken)",
    border: "1px solid var(--border-subtle)",
    borderRadius: "var(--radius-md)",
    width: 260
  },
  searchInput: {
    border: "none",
    background: "none",
    outline: "none",
    flex: 1,
    fontFamily: "var(--font-body)",
    fontSize: 13,
    color: "var(--text-strong)"
  },
  kbd: {
    fontFamily: "var(--font-mono)",
    fontSize: 11,
    color: "var(--text-muted)",
    background: "var(--surface-card)",
    border: "1px solid var(--border-subtle)",
    borderRadius: 5,
    padding: "1px 6px"
  },
  iconBtn: {
    position: "relative",
    width: 38,
    height: 38,
    display: "grid",
    placeItems: "center",
    border: "1px solid var(--border-subtle)",
    background: "var(--surface-card)",
    borderRadius: "var(--radius-md)",
    cursor: "pointer",
    color: "var(--text-body)"
  },
  dot: {
    position: "absolute",
    top: 9,
    right: 9,
    width: 7,
    height: 7,
    borderRadius: "50%",
    background: "var(--danger)",
    border: "2px solid var(--surface-card)"
  },
  avatar: {
    width: 38,
    height: 38,
    borderRadius: "50%",
    border: "none",
    cursor: "pointer",
    background: "var(--accent-subtle)",
    color: "var(--accent-text)",
    fontWeight: 700,
    fontSize: 13,
    fontFamily: "var(--font-body)"
  }
};
Object.assign(window, {
  Icon,
  useLucide,
  Sidebar,
  Topbar
});
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/web/Chrome.jsx", error: String((e && e.message) || e) }); }

// ui_kits/web/DashboardScreen.jsx
try { (() => {
// Vela Web — Dashboard / Overview
function Sparkline({
  data,
  color = "var(--azure-500)",
  fill = true
}) {
  const w = 100,
    h = 36,
    max = Math.max(...data),
    min = Math.min(...data);
  const pts = data.map((d, i) => [i / (data.length - 1) * w, h - (d - min) / (max - min || 1) * (h - 6) - 3]);
  const line = pts.map((p, i) => `${i ? "L" : "M"}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join(" ");
  const area = `${line} L${w} ${h} L0 ${h} Z`;
  const gid = "g" + Math.random().toString(36).slice(2, 7);
  return /*#__PURE__*/React.createElement("svg", {
    viewBox: `0 0 ${w} ${h}`,
    preserveAspectRatio: "none",
    style: {
      width: "100%",
      height: 38
    }
  }, /*#__PURE__*/React.createElement("defs", null, /*#__PURE__*/React.createElement("linearGradient", {
    id: gid,
    x1: "0",
    y1: "0",
    x2: "0",
    y2: "1"
  }, /*#__PURE__*/React.createElement("stop", {
    offset: "0",
    stopColor: color,
    stopOpacity: "0.28"
  }), /*#__PURE__*/React.createElement("stop", {
    offset: "1",
    stopColor: color,
    stopOpacity: "0"
  }))), fill && /*#__PURE__*/React.createElement("path", {
    d: area,
    fill: `url(#${gid})`
  }), /*#__PURE__*/React.createElement("path", {
    d: line,
    fill: "none",
    stroke: color,
    strokeWidth: "2",
    strokeLinecap: "round",
    strokeLinejoin: "round",
    vectorEffect: "non-scaling-stroke"
  }));
}
function AreaChart() {
  const data = [32, 38, 30, 46, 52, 44, 60, 72, 64, 80, 88, 96];
  const w = 560,
    h = 200,
    pad = 8,
    max = 110;
  const x = i => pad + i / (data.length - 1) * (w - pad * 2);
  const y = v => h - pad - v / max * (h - pad * 2);
  const line = data.map((d, i) => `${i ? "L" : "M"}${x(i).toFixed(1)} ${y(d).toFixed(1)}`).join(" ");
  return /*#__PURE__*/React.createElement("svg", {
    viewBox: `0 0 ${w} ${h}`,
    style: {
      width: "100%",
      height: 200
    }
  }, /*#__PURE__*/React.createElement("defs", null, /*#__PURE__*/React.createElement("linearGradient", {
    id: "areaFill",
    x1: "0",
    y1: "0",
    x2: "0",
    y2: "1"
  }, /*#__PURE__*/React.createElement("stop", {
    offset: "0",
    stopColor: "var(--azure-500)",
    stopOpacity: "0.30"
  }), /*#__PURE__*/React.createElement("stop", {
    offset: "1",
    stopColor: "var(--azure-500)",
    stopOpacity: "0"
  }))), [0, 1, 2, 3].map(g => /*#__PURE__*/React.createElement("line", {
    key: g,
    x1: pad,
    x2: w - pad,
    y1: pad + g * ((h - pad * 2) / 3),
    y2: pad + g * ((h - pad * 2) / 3),
    stroke: "var(--border-subtle)",
    strokeWidth: "1"
  })), /*#__PURE__*/React.createElement("path", {
    d: `${line} L${x(data.length - 1)} ${h - pad} L${x(0)} ${h - pad} Z`,
    fill: "url(#areaFill)"
  }), /*#__PURE__*/React.createElement("path", {
    d: line,
    fill: "none",
    stroke: "var(--azure-500)",
    strokeWidth: "2.5",
    strokeLinecap: "round",
    strokeLinejoin: "round"
  }), data.map((d, i) => i === data.length - 1 && /*#__PURE__*/React.createElement("circle", {
    key: i,
    cx: x(i),
    cy: y(d),
    r: "4",
    fill: "var(--azure-500)",
    stroke: "var(--surface-card)",
    strokeWidth: "2"
  })));
}
function DashboardScreen() {
  const {
    Card,
    Badge,
    Avatar,
    Tabs
  } = window.VelaDesignSystem_c1e10b;
  useLucide();
  const metrics = [{
    label: "API 请求",
    value: "1.24M",
    delta: "+12.4%",
    up: true,
    spark: [20, 24, 22, 30, 28, 36, 40],
    color: "var(--azure-500)"
  }, {
    label: "活跃成员",
    value: "284",
    delta: "+6",
    up: true,
    spark: [10, 12, 11, 14, 16, 15, 19],
    color: "var(--cyan-500)"
  }, {
    label: "实时连接",
    value: "3,912",
    delta: "+8.1%",
    up: true,
    spark: [30, 28, 34, 32, 40, 44, 48],
    color: "var(--success)"
  }, {
    label: "错误率",
    value: "0.02%",
    delta: "-0.4%",
    up: false,
    spark: [9, 8, 10, 6, 5, 4, 3],
    color: "var(--warning)"
  }];
  const activity = [{
    who: "Lin Wei",
    what: "部署了 realtime-gateway v2.4",
    when: "2 分钟前",
    icon: "rocket",
    tone: "var(--accent-text)"
  }, {
    who: "Ada Lou",
    what: "邀请了 3 位新成员加入团队",
    when: "1 小时前",
    icon: "user-plus",
    tone: "var(--success)"
  }, {
    who: "系统",
    what: "用量达到本月配额的 60%",
    when: "3 小时前",
    icon: "alert-triangle",
    tone: "var(--warning-text)"
  }, {
    who: "Chen Hao",
    what: "创建了项目 Atlas Analytics",
    when: "昨天",
    icon: "folder-plus",
    tone: "var(--accent-text)"
  }];
  return /*#__PURE__*/React.createElement("div", {
    style: db.root
  }, /*#__PURE__*/React.createElement("div", {
    style: db.head
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: {
      color: "var(--text-muted)",
      fontSize: 14
    }
  }, "\u665A\u4E0A\u597D,Lin \uD83D\uDC4B \u2014 \u8FD9\u662F\u4F60\u56E2\u961F\u4ECA\u5929\u7684\u6982\u51B5\u3002")), /*#__PURE__*/React.createElement(Tabs, {
    items: ["今日", "本周", "本月", "全部"],
    defaultValue: "\u672C\u5468",
    variant: "pill"
  })), /*#__PURE__*/React.createElement("div", {
    style: db.metrics
  }, metrics.map(m => /*#__PURE__*/React.createElement(Card, {
    key: m.label,
    elevation: "raised",
    padding: "md"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      justifyContent: "space-between",
      alignItems: "flex-start"
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      fontSize: 13,
      color: "var(--text-muted)",
      fontWeight: 500
    }
  }, m.label), /*#__PURE__*/React.createElement(Badge, {
    color: m.up ? "success" : "warning"
  }, m.delta)), /*#__PURE__*/React.createElement("div", {
    style: {
      fontFamily: "var(--font-display)",
      fontSize: 30,
      fontWeight: 700,
      color: "var(--text-strong)",
      margin: "8px 0 4px",
      letterSpacing: "-0.02em"
    }
  }, m.value), /*#__PURE__*/React.createElement(Sparkline, {
    data: m.spark,
    color: m.color
  })))), /*#__PURE__*/React.createElement("div", {
    style: db.grid
  }, /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "lg"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      justifyContent: "space-between",
      alignItems: "center",
      marginBottom: 18
    }
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 16,
      fontWeight: 700,
      color: "var(--text-strong)"
    }
  }, "\u8BF7\u6C42\u91CF\u8D8B\u52BF"), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 13,
      color: "var(--text-muted)",
      marginTop: 2
    }
  }, "\u8FC7\u53BB 12 \u4E2A\u6708 \xB7 \u5355\u4F4D\u767E\u4E07")), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 6,
      fontSize: 13,
      color: "var(--success-text)",
      fontWeight: 600
    }
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "trending-up",
    size: 16
  }), " +28% \u540C\u6BD4")), /*#__PURE__*/React.createElement(AreaChart, null)), /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "lg"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 16,
      fontWeight: 700,
      color: "var(--text-strong)",
      marginBottom: 14
    }
  }, "\u6700\u8FD1\u52A8\u6001"), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column",
      gap: 4
    }
  }, activity.map((a, i) => /*#__PURE__*/React.createElement("div", {
    key: i,
    style: db.activity
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      ...db.actIcon,
      color: a.tone
    }
  }, /*#__PURE__*/React.createElement(Icon, {
    name: a.icon,
    size: 16
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1,
      minWidth: 0
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 13.5,
      color: "var(--text-strong)"
    }
  }, /*#__PURE__*/React.createElement("b", {
    style: {
      fontWeight: 600
    }
  }, a.who), " ", a.what), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 12,
      color: "var(--text-faint)",
      marginTop: 1
    }
  }, a.when))))))));
}
const db = {
  root: {
    padding: 28,
    display: "flex",
    flexDirection: "column",
    gap: 20
  },
  head: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center"
  },
  metrics: {
    display: "grid",
    gridTemplateColumns: "repeat(4, 1fr)",
    gap: 16
  },
  grid: {
    display: "grid",
    gridTemplateColumns: "1.6fr 1fr",
    gap: 16
  },
  activity: {
    display: "flex",
    gap: 12,
    padding: "9px 0",
    borderBottom: "1px solid var(--border-subtle)"
  },
  actIcon: {
    width: 30,
    height: 30,
    flex: "none",
    borderRadius: "var(--radius-md)",
    background: "var(--surface-sunken)",
    display: "grid",
    placeItems: "center"
  }
};
Object.assign(window, {
  DashboardScreen,
  Sparkline
});
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/web/DashboardScreen.jsx", error: String((e && e.message) || e) }); }

// ui_kits/web/LoginScreen.jsx
try { (() => {
// Vela Web — Login screen
function LoginScreen({
  onLogin
}) {
  const {
    Button,
    Input,
    Checkbox
  } = window.VelaDesignSystem_c1e10b;
  useLucide();
  return /*#__PURE__*/React.createElement("div", {
    style: lg.root
  }, /*#__PURE__*/React.createElement("div", {
    style: lg.left
  }, /*#__PURE__*/React.createElement("div", {
    style: lg.brand
  }, /*#__PURE__*/React.createElement("img", {
    src: "../../assets/vela-logo-dark.svg",
    width: "120",
    alt: "Vela"
  })), /*#__PURE__*/React.createElement("div", {
    style: lg.hero
  }, /*#__PURE__*/React.createElement("div", {
    style: lg.eyebrow
  }, "REALTIME COLLABORATION"), /*#__PURE__*/React.createElement("h2", {
    style: lg.heroTitle
  }, "\u4EE5\u5149\u901F", /*#__PURE__*/React.createElement("br", null), "\u6784\u5EFA\u4F60\u7684\u56E2\u961F"), /*#__PURE__*/React.createElement("p", {
    style: lg.heroSub
  }, "\u6570\u636E\u3001\u6587\u6863\u4E0E\u5BF9\u8BDD,\u5728\u540C\u4E00\u5904\u5B9E\u65F6\u540C\u6B65\u3002")), /*#__PURE__*/React.createElement("div", {
    style: lg.stats
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: lg.statN
  }, "99.98%"), /*#__PURE__*/React.createElement("div", {
    style: lg.statL
  }, "\u53EF\u7528\u6027")), /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: lg.statN
  }, "12ms"), /*#__PURE__*/React.createElement("div", {
    style: lg.statL
  }, "p50 \u5EF6\u8FDF")), /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    style: lg.statN
  }, "40k+"), /*#__PURE__*/React.createElement("div", {
    style: lg.statL
  }, "\u56E2\u961F")))), /*#__PURE__*/React.createElement("div", {
    style: lg.right
  }, /*#__PURE__*/React.createElement("div", {
    style: lg.form
  }, /*#__PURE__*/React.createElement("img", {
    src: "../../assets/vela-mark.svg",
    width: "40",
    height: "40",
    alt: "",
    style: {
      marginBottom: 22
    }
  }), /*#__PURE__*/React.createElement("h1", {
    style: lg.title
  }, "\u6B22\u8FCE\u56DE\u6765"), /*#__PURE__*/React.createElement("p", {
    style: lg.sub
  }, "\u767B\u5F55\u4EE5\u7EE7\u7EED\u4F7F\u7528 Vela \u5DE5\u4F5C\u53F0"), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column",
      gap: 14,
      marginTop: 26
    }
  }, /*#__PURE__*/React.createElement(Input, {
    label: "\u5DE5\u4F5C\u90AE\u7BB1",
    placeholder: "you@company.com",
    defaultValue: "lin.wei@acme.com",
    iconLeft: /*#__PURE__*/React.createElement(Icon, {
      name: "mail",
      size: 16
    })
  }), /*#__PURE__*/React.createElement(Input, {
    label: "\u5BC6\u7801",
    type: "password",
    placeholder: "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022",
    defaultValue: "velarocks",
    iconLeft: /*#__PURE__*/React.createElement(Icon, {
      name: "lock",
      size: 16
    })
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      justifyContent: "space-between",
      alignItems: "center"
    }
  }, /*#__PURE__*/React.createElement(Checkbox, {
    label: "\u4FDD\u6301\u767B\u5F55",
    defaultChecked: true
  }), /*#__PURE__*/React.createElement("a", {
    href: "#",
    style: {
      fontSize: 13,
      fontWeight: 500
    }
  }, "\u5FD8\u8BB0\u5BC6\u7801?")), /*#__PURE__*/React.createElement(Button, {
    size: "lg",
    fullWidth: true,
    onClick: onLogin,
    iconRight: /*#__PURE__*/React.createElement(Icon, {
      name: "arrow-right",
      size: 18
    })
  }, "\u767B\u5F55"), /*#__PURE__*/React.createElement(Button, {
    size: "lg",
    variant: "secondary",
    fullWidth: true,
    iconLeft: /*#__PURE__*/React.createElement(Icon, {
      name: "github",
      size: 18
    })
  }, "\u4F7F\u7528 GitHub \u767B\u5F55")), /*#__PURE__*/React.createElement("p", {
    style: lg.foot
  }, "\u8FD8\u6CA1\u6709\u8D26\u6237? ", /*#__PURE__*/React.createElement("a", {
    href: "#",
    style: {
      fontWeight: 600
    }
  }, "\u514D\u8D39\u6CE8\u518C")))));
}
const lg = {
  root: {
    display: "flex",
    height: "100%",
    background: "var(--surface-page)"
  },
  left: {
    flex: "1 1 0",
    background: "linear-gradient(160deg, #0f1830 0%, #0a0c14 55%, #0b1f2a 100%)",
    padding: "48px 52px",
    display: "flex",
    flexDirection: "column",
    justifyContent: "space-between",
    position: "relative",
    overflow: "hidden"
  },
  brand: {},
  hero: {},
  eyebrow: {
    fontFamily: "var(--font-mono)",
    fontSize: 12,
    letterSpacing: "0.09em",
    color: "var(--cyan-400)",
    marginBottom: 18
  },
  heroTitle: {
    fontFamily: "var(--font-display)",
    fontSize: 46,
    lineHeight: 1.08,
    color: "#fff",
    fontWeight: 700,
    letterSpacing: "-0.03em",
    margin: 0
  },
  heroSub: {
    color: "#aeb8cc",
    fontSize: 17,
    marginTop: 18,
    maxWidth: 360
  },
  stats: {
    display: "flex",
    gap: 40
  },
  statN: {
    fontFamily: "var(--font-display)",
    fontSize: 26,
    fontWeight: 700,
    color: "#fff"
  },
  statL: {
    fontSize: 13,
    color: "#8893a8",
    marginTop: 2
  },
  right: {
    flex: "1 1 0",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    padding: 40
  },
  form: {
    width: "100%",
    maxWidth: 380
  },
  title: {
    fontSize: 30,
    fontWeight: 700,
    color: "var(--text-strong)"
  },
  sub: {
    color: "var(--text-muted)",
    marginTop: 6
  },
  foot: {
    textAlign: "center",
    marginTop: 22,
    fontSize: 14,
    color: "var(--text-muted)"
  }
};
Object.assign(window, {
  LoginScreen
});
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/web/LoginScreen.jsx", error: String((e && e.message) || e) }); }

// ui_kits/web/ProjectsScreen.jsx
try { (() => {
// Vela Web — Projects (data table) screen
function ProjectsScreen() {
  const {
    Card,
    Badge,
    Avatar,
    Button,
    Input
  } = window.VelaDesignSystem_c1e10b;
  useLucide();
  const rows = [{
    name: "Atlas Analytics",
    env: "Production",
    status: "运行中",
    tone: "success",
    reqs: "428K",
    team: ["LW", "AL", "CH"],
    updated: "2 分钟前"
  }, {
    name: "Realtime Gateway",
    env: "Production",
    status: "运行中",
    tone: "success",
    reqs: "1.1M",
    team: ["CH", "MZ"],
    updated: "12 分钟前"
  }, {
    name: "Nova Search",
    env: "Staging",
    status: "部署中",
    tone: "accent",
    reqs: "62K",
    team: ["AL"],
    updated: "1 小时前"
  }, {
    name: "Pulse Notifications",
    env: "Production",
    status: "降级",
    tone: "warning",
    reqs: "240K",
    team: ["LW", "MZ", "CH", "AL"],
    updated: "3 小时前"
  }, {
    name: "Ledger Sync",
    env: "Development",
    status: "已暂停",
    tone: "neutral",
    reqs: "—",
    team: ["MZ"],
    updated: "昨天"
  }];
  return /*#__PURE__*/React.createElement("div", {
    style: pj.root
  }, /*#__PURE__*/React.createElement("div", {
    style: pj.toolbar
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      width: 280
    }
  }, /*#__PURE__*/React.createElement(Input, {
    iconLeft: /*#__PURE__*/React.createElement(Icon, {
      name: "search",
      size: 16
    }),
    placeholder: "\u641C\u7D22\u9879\u76EE\u2026"
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      gap: 10,
      marginLeft: "auto"
    }
  }, /*#__PURE__*/React.createElement(Button, {
    variant: "secondary",
    iconLeft: /*#__PURE__*/React.createElement(Icon, {
      name: "filter",
      size: 16
    })
  }, "\u7B5B\u9009"), /*#__PURE__*/React.createElement(Button, {
    iconLeft: /*#__PURE__*/React.createElement(Icon, {
      name: "plus",
      size: 16
    })
  }, "\u65B0\u5EFA\u9879\u76EE"))), /*#__PURE__*/React.createElement(Card, {
    elevation: "raised",
    padding: "sm"
  }, /*#__PURE__*/React.createElement("table", {
    style: pj.table
  }, /*#__PURE__*/React.createElement("thead", null, /*#__PURE__*/React.createElement("tr", null, ["项目", "环境", "状态", "请求量", "团队", "更新", ""].map(h => /*#__PURE__*/React.createElement("th", {
    key: h,
    style: pj.th
  }, h)))), /*#__PURE__*/React.createElement("tbody", null, rows.map(r => /*#__PURE__*/React.createElement("tr", {
    key: r.name,
    style: pj.tr
  }, /*#__PURE__*/React.createElement("td", {
    style: pj.td
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 10
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: pj.proj
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "box",
    size: 16
  })), /*#__PURE__*/React.createElement("span", {
    style: {
      fontWeight: 600,
      color: "var(--text-strong)"
    }
  }, r.name))), /*#__PURE__*/React.createElement("td", {
    style: pj.td
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      fontFamily: "var(--font-mono)",
      fontSize: 12,
      color: "var(--text-muted)"
    }
  }, r.env)), /*#__PURE__*/React.createElement("td", {
    style: pj.td
  }, /*#__PURE__*/React.createElement(Badge, {
    color: r.tone,
    dot: true
  }, r.status)), /*#__PURE__*/React.createElement("td", {
    style: {
      ...pj.td,
      fontFamily: "var(--font-mono)",
      color: "var(--text-body)"
    }
  }, r.reqs), /*#__PURE__*/React.createElement("td", {
    style: pj.td
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex"
    }
  }, r.team.map((t, i) => /*#__PURE__*/React.createElement("span", {
    key: i,
    style: {
      ...pj.team,
      marginLeft: i ? -8 : 0
    }
  }, t)))), /*#__PURE__*/React.createElement("td", {
    style: {
      ...pj.td,
      color: "var(--text-faint)",
      fontSize: 13
    }
  }, r.updated), /*#__PURE__*/React.createElement("td", {
    style: pj.td
  }, /*#__PURE__*/React.createElement("button", {
    style: pj.more
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "more-horizontal",
    size: 18
  })))))))));
}
const pj = {
  root: {
    padding: 28,
    display: "flex",
    flexDirection: "column",
    gap: 18
  },
  toolbar: {
    display: "flex",
    alignItems: "center",
    gap: 12
  },
  table: {
    width: "100%",
    borderCollapse: "collapse"
  },
  th: {
    textAlign: "left",
    fontSize: 12,
    fontWeight: 600,
    color: "var(--text-muted)",
    padding: "10px 14px",
    borderBottom: "1px solid var(--border-subtle)",
    whiteSpace: "nowrap"
  },
  tr: {
    transition: "var(--transition-colors)"
  },
  td: {
    padding: "13px 14px",
    borderBottom: "1px solid var(--border-subtle)",
    fontSize: 14,
    verticalAlign: "middle"
  },
  proj: {
    width: 30,
    height: 30,
    flex: "none",
    borderRadius: "var(--radius-md)",
    background: "var(--accent-subtle)",
    color: "var(--accent-text)",
    display: "grid",
    placeItems: "center"
  },
  team: {
    width: 26,
    height: 26,
    borderRadius: "50%",
    background: "var(--surface-sunken)",
    border: "2px solid var(--surface-card)",
    display: "grid",
    placeItems: "center",
    fontSize: 10,
    fontWeight: 600,
    color: "var(--text-body)"
  },
  more: {
    border: "none",
    background: "none",
    cursor: "pointer",
    color: "var(--text-muted)",
    padding: 4,
    borderRadius: 6,
    display: "grid",
    placeItems: "center"
  }
};
Object.assign(window, {
  ProjectsScreen
});
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/web/ProjectsScreen.jsx", error: String((e && e.message) || e) }); }

__ds_ns.Alert = __ds_scope.Alert;

__ds_ns.Avatar = __ds_scope.Avatar;

__ds_ns.Badge = __ds_scope.Badge;

__ds_ns.Card = __ds_scope.Card;

__ds_ns.Spinner = __ds_scope.Spinner;

__ds_ns.Tabs = __ds_scope.Tabs;

__ds_ns.Button = __ds_scope.Button;

__ds_ns.Checkbox = __ds_scope.Checkbox;

__ds_ns.Input = __ds_scope.Input;

__ds_ns.Select = __ds_scope.Select;

__ds_ns.Switch = __ds_scope.Switch;

})();

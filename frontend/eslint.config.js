import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import reactHooks from 'eslint-plugin-react-hooks'

/**
 * 这个仓库之前没有 lint,所以这份配置的重点不是"越严越好",而是**让剩下的每一条
 * 都值得看**。关掉的两类不是偷懒,是它们在这里报的都不是问题:
 *
 * - `no-explicit-any` 的 166 条全在 `api/modules/*`,来自 `http.get<any, Envelope<T>>`
 *   这个惯用法 —— `any` 是 axios 泛型的**第一个类型参数占位**(请求体类型,这里用不到),
 *   真正的返回类型在第二个参数上,是精确的。把它改成 `unknown` 要动 17 个文件里的
 *   每一处调用,换不来任何类型安全。
 * - `no-control-regex` 的 11 条全在终端与结果渲染代码,而那些代码**存在的意义**就是
 *   处理控制字符(转义序列、退格、回车)。在那里禁用控制字符正则等于禁用这个功能。
 *
 * 其余一律保留 —— 尤其是 react-hooks 那组,它是引入 ESLint 的主要理由。
 */
export default tseslint.config(
  { ignores: ['dist/**', 'node_modules/**', 'test-results/**', 'playwright-report/**'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    plugins: { 'react-hooks': reactHooks },
    rules: reactHooks.configs.recommended.rules,
  },
  {
    // axios 泛型的第一个参数在这里永远用不到,写 any 是它的惯用形态。
    files: ['src/api/**/*.ts'],
    rules: { '@typescript-eslint/no-explicit-any': 'off' },
  },
  {
    // 这些文件的职责就是渲染/解析控制字符。
    files: ['src/lib/transcript.ts', 'src/lib/sqlResult.ts'],
    rules: { 'no-control-regex': 'off' },
  },
  {
    // 测试要构造畸形输入(控制字符、异常形状),也常用 `_` 丢弃解构出来的字段。
    files: ['tests/**/*.ts', 'e2e/**/*.ts'],
    rules: {
      'no-control-regex': 'off',
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-unused-vars': ['error', { varsIgnorePattern: '^_', argsIgnorePattern: '^_' }],
    },
  },
)

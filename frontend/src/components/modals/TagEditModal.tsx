import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, X } from 'lucide-react'
import { Modal } from '@/components/common/Modal'
import { Button } from '@/components/common/Button'

/**
 * 标签选择器 —— 角色、用户、实例三处共用的那一个。
 *
 * ## 它为什么允许自由输入
 *
 * 候选来自 `GET /tags`,而那不是一张受管的字典表:服务端扫的是**所有实例的 `tags`
 * 字段**,切分去重之后回来。也就是说它回答的是"别人已经用过什么",不是"可以用什么"。
 * 一个还没有任何实例贴过的新标签当然是合法的 —— 先给角色授权、再去建实例,是很正常
 * 的顺序。所以输入框是自由的,下面那排只是省几次打字。
 *
 * ## 草稿是本地的
 *
 * 进来先把 `tags` 拷一份到本地,加加减减都只动这份草稿,点「保存」才把整份交出去。
 * 取消/按 Esc/点遮罩都原样丢弃 —— 一个改了一半就已经生效的权限编辑器,会让人不敢
 * 点开它看看。
 *
 * 保存是**整表覆盖**(三个接口都是),所以交出去的就是最终态,包括空数组。空数组在
 * 用户那一侧是有含义的:清掉按人授权,回落到角色。
 */
export function TagEditModal({
  open, title, sub, tags, suggestions, busy, onClose, onSave,
}: {
  open: boolean
  title: string
  sub?: string
  tags: string[]
  suggestions: string[]
  busy?: boolean
  onClose: () => void
  onSave: (tags: string[]) => void
}) {
  const { t } = useTranslation()
  // key 由调用方绑在 open 上,所以这里的初值就是每次打开时的当前值。
  const [list, setList] = useState<string[]>(tags)
  const [draft, setDraft] = useState('')

  /**
   * 归一化成服务端存的样子:小写、去空白。
   *
   * 后端 `SplitTags` 按逗号切、`ToLower` + `TrimSpace`,所以这里必须同样处理 ——
   * 不然 `Core` 和 `core` 在界面上是两个标签、在库里是一个。
   *
   * 与 Vue 版的一处有意不同:那边把逗号**删掉**,于是粘进来一串 `core, payment`
   * 会变成一个叫 `corepayment` 的标签。这里按逗号/空白**切开**,一次加进去几个 ——
   * 从别处拷一行标签过来是这个框最常见的用法。
   */
  const parse = (raw: string): string[] =>
    raw.toLowerCase().split(/[,\s]+/).map((s) => s.trim()).filter(Boolean)

  function add(raw: string) {
    const incoming = parse(raw).filter((v) => !list.includes(v))
    if (incoming.length) setList([...list, ...incoming])
    setDraft('')
  }

  const remove = (tag: string) => setList(list.filter((x) => x !== tag))
  const free = suggestions.filter((s) => !list.includes(s))

  return (
    <Modal
      open={open}
      title={title}
      sub={sub}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button variant="primary" disabled={busy} onClick={() => onSave(list)}>{t('save')}</Button>
        </>
      }
    >
      <div className="tagpick-chips">
        {list.map((tag) => (
          <span key={tag} className="tagpick-chip">
            {tag}
            <button type="button" className="tagpick-cx" title={t('tagRemove')} onClick={() => remove(tag)}>
              <X size={11} />
            </button>
          </span>
        ))}
        {!list.length && <span className="cell-sub">{t('tagNone')}</span>}
      </div>

      <div className="tagpick-inrow">
        <input
          value={draft}
          spellCheck={false}
          placeholder={t('tagAddPh')}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); add(draft) } }}
        />
        <button
          type="button"
          className="tagpick-addb"
          disabled={!parse(draft).length}
          title={t('tagAdd')}
          onClick={() => add(draft)}
        >
          <Plus size={15} />
        </button>
      </div>

      {free.length > 0 && (
        <>
          <div className="tagpick-sl">{t('tagSuggest')}</div>
          <div className="tagpick-sugs">
            {free.map((s) => (
              <button key={s} type="button" className="tagpick-sug" onClick={() => add(s)}>
                <Plus size={11} />{s}
              </button>
            ))}
          </div>
        </>
      )}
    </Modal>
  )
}

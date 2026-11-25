// LOGO 的内容:配置了图片就显示图片,没配置显示原来的帆船图标。
// 外框(尺寸、圆角、渐变底)由使用处自己定;配置了图片时,使用处给外框加 .custom 去掉渐变底。
import { Sailboat } from 'lucide-react'
import { useBrand } from '@/hooks/useBrand'
import './BrandMark.scoped.css'

export default function BrandMark({ size }: { size: number }) {
  const { logo } = useBrand()
  return logo
    ? <img src={logo} alt="" className="brand-img" data-testid="brand-logo-img" />
    : <Sailboat size={size} color="#fff" data-testid="brand-logo-default" />
}

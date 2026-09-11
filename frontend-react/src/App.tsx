import { RouterProvider } from 'react-router-dom'
import { router } from '@/router'
import Toasts from '@/components/common/Toasts'

export default function App() {
  return (
    <>
      <RouterProvider router={router} />
      <Toasts />
    </>
  )
}

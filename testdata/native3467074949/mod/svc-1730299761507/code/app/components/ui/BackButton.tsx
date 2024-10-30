'use client'

import { useRouter } from 'next/navigation'

const backButtonStyles = {
  button: "inline-flex items-center px-4 py-2 border border-transparent text-sm font-medium rounded-md text-gray-700 bg-gray-100 hover:bg-gray-200 dark:text-gray-200 dark:bg-gray-700 dark:hover:bg-gray-600 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-accent-yellow",
}

export function BackButton() {
  const router = useRouter()

  return (
    <button onClick={() => router.back()} className={backButtonStyles.button}>
      ← Back
    </button>
  )
}

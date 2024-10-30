'use client'

import { useTheme } from "next-themes"
import { useState, useEffect } from "react"
import { SunIcon, MoonIcon } from "@heroicons/react/24/solid"

const themeToggleStyles = {
    button: "p-2 rounded-full bg-gray-200 dark:bg-gray-700 text-gray-800 dark:text-gray-200 hover:bg-gray-300 dark:hover:bg-gray-600 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-accent-yellow",
    icon: "w-5 h-5",
} as const;

export function ThemeToggle() {
    const [mounted, setMounted] = useState(false)
    const { theme, setTheme } = useTheme()

    useEffect(() => setMounted(true), [])

    if (!mounted) return null

    return (
        <button
            onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
            className={themeToggleStyles.button}
            aria-label="Toggle theme"
        >
            {theme === 'dark' ? (
                <SunIcon className={themeToggleStyles.icon} />
            ) : (
                <MoonIcon className={themeToggleStyles.icon} />
            )}
        </button>
    )
}

import { Inter } from 'next/font/google'
import { Fira_Code } from 'next/font/google'

export const fontSans = Inter({ 
    subsets: ['latin'],
    variable: '--font-sans',
    display: 'swap',
    preload: true,
    fallback: [
        '-apple-system',
        'BlinkMacSystemFont',
        'Segoe UI',
        'Roboto',
        'Helvetica Neue',
        'sans-serif'
    ]
})

export const fontMono = Fira_Code({
    subsets: ['latin'],
    variable: '--font-mono',
    display: 'swap',
    preload: true,
    fallback: [
        'SFMono-Regular',
        'Menlo',
        'Monaco',
        'Consolas',
        'Liberation Mono',
        'Courier New',
        'monospace'
    ]
})

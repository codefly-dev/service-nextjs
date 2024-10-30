import '@/app/globals.css'
import React from "react";
import {Providers} from './providers'
import { fontSans } from "@/app/libs/fonts"
import { cn } from "@/lib/utils"
import { Header } from '@/app/components/layout/Header';
import { ThemeProvider } from 'next-themes'

export default function RootLayout({
                                       children,
                                   }: {
    children: React.ReactNode
}) {
    return (
        <html lang="en" suppressHydrationWarning>
        <head ><title>codefly</title></head>
        <body
            className={cn(
                "min-h-screen bg-background font-sans antialiased",
                fontSans.variable
            )}
        >
        <Providers>
            <Header/>
            <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
                {children}
            </ThemeProvider>
        </Providers>
        </body>
        </html>
    )
}

'use client'

import { useEffect, useState } from 'react'
import { useSession } from "next-auth/react"
import { UserButton } from "@/app/components/layout/UserButton"
import { SignInButton } from "@/app/components/layout/SignInButton"
import { SupportedAuthType } from "@/lib/auth/config"

export function SessionProvider() {
    const [authType, setAuthType] = useState<SupportedAuthType>('none')
    const { data: session, status } = useSession()

    useEffect(() => {
        setAuthType((process.env.NEXT_PUBLIC_AUTH_TYPE as SupportedAuthType) || 'none')
    }, [authType, status, session])

    if (authType === 'none') return null

    if (status === 'loading') return <div>Loading...</div>

    
    if (session?.user) {
        return <UserButton user={session.user} />
    } else {
        return <SignInButton />
    }
}

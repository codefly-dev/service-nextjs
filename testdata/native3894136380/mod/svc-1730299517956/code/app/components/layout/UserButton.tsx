'use client'

import { User } from "next-auth"
import { signOut } from "next-auth/react"
import Image from "next/image"
import { useState, useRef, useEffect } from "react"
import Link from "next/link"

const styles = {
    container: "relative rounded-full",
    button: "flex items-center space-x-2 p-2 rounded-full hover:bg-yellow-400 hover:text-black",
    userName: "px-1",
    dropdown: "absolute right-0 mt-2 w-48 bg-white dark:bg-gray-800 rounded-md overflow-hidden shadow-xl z-10 border border-gray-200 dark:border-gray-700",
    dropdownItem: "block px-4 py-2 text-sm text-gray-700 dark:text-gray-200 hover:bg-yellow-400 hover:text-black",
    signOutButton: "block w-full text-left px-4 py-2 text-sm text-gray-700 dark:text-gray-200 hover:bg-yellow-400 hover:text-black",
    fallbackImage: "w-8 h-8 rounded-full bg-gray-200 dark:bg-gray-700 flex items-center justify-center text-gray-700 dark:text-gray-200",
    image: "rounded-full",
}

interface UserButtonProps {
    user: User
}

export function UserButton({ user }: UserButtonProps) {
    const [imageError, setImageError] = useState(false)
    const [isOpen, setIsOpen] = useState(false)
    const dropdownRef = useRef<HTMLDivElement>(null)

    useEffect(() => {
        const handleClickOutside = (event: MouseEvent) => {
            if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
                setIsOpen(false)
            }
        }

        document.addEventListener('mousedown', handleClickOutside)
        return () => {
            document.removeEventListener('mousedown', handleClickOutside)
        }
    }, [user.image])

    return (
        <div className={styles.container} ref={dropdownRef}>
            <button 
                onClick={() => setIsOpen(!isOpen)} 
                className={styles.button}
            >
                {!imageError ? (
                    <Image
                        src={user.image || `https://ui-avatars.com/api/?name=${encodeURIComponent(user.name || 'User')}`}
                        alt={user.name || "User avatar"}
                        width={32}
                        height={32}
                        className={styles.image}
                        onError={() => setImageError(true)}
                    />
                ) : (
                    <div className={styles.fallbackImage}>
                        {user.name ? user.name[0].toUpperCase() : 'U'}
                    </div>
                )}
                <span className={styles.userName}>{user.name}</span>
            </button>
            {isOpen && (
                <div className={styles.dropdown}>
                    <Link href="/profile" className={styles.dropdownItem}>
                        Profile
                    </Link>
                    <button 
                        onClick={() => signOut()} 
                        className={styles.signOutButton}
                    >
                        Sign out
                    </button>
                </div>
            )}
        </div>
    )
}

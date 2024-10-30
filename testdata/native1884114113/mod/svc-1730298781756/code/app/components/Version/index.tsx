'use client';

import React, { useState, useEffect } from 'react';
import { styles } from './styles';

interface VersionData {
    version: string;
}

interface VersionProps {
    url: string;
}

export function Version({ url: url }: VersionProps) {
    const [version, setVersion] = useState<string | null>(null);
    const [error, setError] = useState<string | null>(null);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        async function checkVersion() {
            try {
                const data = await (await fetch(url)).json();
                setVersion(data.version);
                setError(null);
            } catch (error) {
                console.error('Error fetching version:', error);
                let errorMessage = 'Failed to fetch version information';
                
                if (error instanceof Error) {
                    if (error.message.includes('Route not found')) {
                        errorMessage = 'Version endpoint not available';
                    } else if (error.message.includes('Failed to fetch')) {
                        errorMessage = 'Server is not responding';
                    }
                }
                
                setError(errorMessage);
                setVersion(null);
            } finally {
                setLoading(false);
            }
        }

        checkVersion();
    }, [url]);

    if (error) {
        return (
            <div className={styles.container}>
                <h2 className={styles.title}>Server Status</h2>
                <div className={styles.errorContainer}>
                    <p className={styles.error}>{error}</p>
                    <p className={styles.errorHint}>
                        Please ensure the server is running and the version endpoint is configured.
                    </p>
                </div>
            </div>
        );
    }

    return (
        <div className={styles.container}>
            <h2 className={styles.title}>Server Version</h2>
            {loading ? (
                <p className={styles.loading}>Loading version information...</p>
            ) : (
                <p className={styles.version}>Current version: {version}</p>
            )}
        </div>
    );
} 

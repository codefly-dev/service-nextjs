'use client';

import React, { useState } from 'react';
import { ChevronDownIcon, ChevronRightIcon } from '@heroicons/react/24/solid';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism';
import { getSession } from "next-auth/react";

const styles = {
    container: "bg-gray-50 dark:bg-gray-900 rounded p-4",
    header: "flex flex-col mb-3",
    routeInfo: "flex-1",
    routeTitle: "font-medium text-gray-900 dark:text-gray-100 flex items-center cursor-pointer",
    routeLabel: "bg-green-100 text-green-800 text-xs px-2 py-1 rounded mr-2",
    url: "text-sm text-gray-600 dark:text-gray-400 mt-1",
    methodButtons: "flex gap-2 mt-3",
    buttonGET: "px-4 py-2 bg-blue-500 hover:bg-blue-600 text-white rounded transition-colors",
    buttonPOST: "px-4 py-2 bg-green-500 hover:bg-green-600 text-white rounded transition-colors",
    codeSection: "mt-4",
    codeBlock: "rounded-lg overflow-hidden w-full",
    codeFont: "font-mono",
    responseSection: "mt-6",
    responseHeader: "flex items-center gap-2 mb-3",
    responseTitle: "font-medium text-gray-600 dark:text-gray-400",
    responseContent: "bg-white dark:bg-gray-800 p-4 rounded overflow-auto",
    chevron: "w-5 h-5 mr-2",
    content: "mt-2",
    badge: "text-xs font-semibold px-2.5 py-0.5 rounded-full",
    badgeSuccess: "bg-green-100 text-green-800",
    badgeError: "bg-red-100 text-red-800",
    badgeWarning: "bg-yellow-100 text-yellow-800",
}

interface RouteItemProps {
    route: {
        path: string;
        method: string;
        visibility: string;
    };
    serviceAddress: string;
    serviceName: string;
    moduleName: string;
}


export default function RouteItem({ route, serviceAddress, serviceName, moduleName }: RouteItemProps) {
    const [response, setResponse] = useState<any>(null);
    const [statusCode, setStatusCode] = useState<number | null>(null);
    const [isExpanded, setIsExpanded] = useState(true);



    const callEndpoint = async () => {
        try {
            const session = await getSession();
            const headers: Record<string, string> = {
                'Content-Type': 'application/json',
            };
    
            // Check for token in session.token or session.user.token
            const token = session?.clientToken;
            if (token) {
                headers['Authorization'] = `Bearer ${token}`;
            }
            console.log('TOKEN:', token);
    
            const req = {
                method: route.method,
                headers,
            };
            const url = `${serviceAddress}${route.path}`;
            const response = await fetch(url, req);
            setStatusCode(response.status);
            
            // Try to parse JSON, fallback to text if it fails
            let data;
            const textResponse = await response.text();
            try {
                data = JSON.parse(textResponse);
            } catch (e) {
                data = { message: textResponse };
            }
            setResponse(data);
        } catch (error) {
            console.error('Error calling endpoint:', error);
            setStatusCode(500);
            setResponse({
                error: error instanceof Error ? error.message : 'An unknown error occurred'
            });
        }
    };

    const getButtonClassName = () => {
        return route.method === 'GET' ? styles.buttonGET : styles.buttonPOST;
    };

    const getCodeExample = () => 
`// Get the endpoint URL server-side
const url = endpoint({
    service: '${serviceName}',
    path:    '${route.path}',
    method:  '${route.method}'
});`;

    const fullUrl = `${serviceAddress}${route.path}`;

    const getStatusBadgeClass = (status: number | null) => {
        if (!status) return '';
        if (status >= 200 && status < 300) return styles.badgeSuccess;
        if (status >= 400 && status < 500) return styles.badgeWarning;
        return styles.badgeError;
    };

    return (
        <div className={styles.container}>
            <div className={styles.header}>
                <div className={styles.routeInfo}>
                    <h4 
                        className={styles.routeTitle}
                        onClick={() => setIsExpanded(!isExpanded)}
                    >
                        {isExpanded ? (
                            <ChevronDownIcon className={styles.chevron} />
                        ) : (
                            <ChevronRightIcon className={styles.chevron} />
                        )}
                        <span className={styles.routeLabel}>Path</span>
                        {route.path}
                    </h4>
                    {isExpanded && (
                        <div className={styles.content}>
                            <div className={styles.url}>{fullUrl}</div>
                            <div className={styles.methodButtons}>
                                <button
                                    onClick={callEndpoint}
                                    className={getButtonClassName()}
                                >
                                    {route.method}
                                </button>
                            </div>
                            <div className={styles.codeSection}>
                                <div className={styles.codeBlock}>
                                    <SyntaxHighlighter 
                                        language="typescript"
                                        style={oneDark}
                                        customStyle={{
                                            padding: '1.5rem',
                                            borderRadius: '0.5rem',
                                            margin: 0,
                                        }}
                                    >
                                        {getCodeExample()}
                                    </SyntaxHighlighter>
                                </div>
                            </div>
                            {response && (
                                <div className={styles.responseSection}>
                                    <div className={styles.responseHeader}>
                                        {statusCode && (
                                            <span className={`${styles.badge} ${getStatusBadgeClass(statusCode)}`}>
                                                {statusCode}
                                            </span>
                                        )}
                                        <h5 className={styles.responseTitle}>Response Data</h5>
                                    </div>
                                    <pre className={styles.responseContent}>
                                        {statusCode && statusCode >= 400 
                                            ? response.error || response.message || 'Request failed'
                                            : JSON.stringify(response, null, 2)
                                        }
                                    </pre>
                                </div>
                            )}
                        </div>
                    )}
                </div>
            </div>
        </div>
    );
} 

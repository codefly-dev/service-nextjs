'use client';

import React, { useState } from 'react';
import { ServiceEndpoint } from 'codefly';
import { endpoint, fetchEndpoint } from 'codefly';



const styles = {
    container: "bg-gray-50 dark:bg-gray-900 rounded p-4",
    header: "flex justify-between items-start mb-3",
    nameSection: "flex-1",
    name: "font-medium text-gray-900 dark:text-gray-100",
    methodRoute: "text-sm text-gray-600 dark:text-gray-400 mt-1",
    url: "text-sm text-gray-600 dark:text-gray-400 mt-1",
    codeBlock: "bg-gray-800 text-gray-200 p-3 rounded text-sm font-mono mt-2",
    buttonGET: "px-4 py-2 bg-blue-500 hover:bg-blue-600 text-white rounded transition-colors",
    buttonPOST: "px-4 py-2 bg-green-500 hover:bg-green-600 text-white rounded transition-colors",
    responseSection: "mt-4",
    responseTitle: "font-medium mb-2",
    responseContent: "bg-white dark:bg-gray-800 p-3 rounded overflow-auto",
}

interface EndpointItemProps {
    endpoint: ServiceEndpoint;
    moduleName: string;
}

export default function EndpointItem({ endpoint, moduleName }: EndpointItemProps) {
    const [response, setResponse] = useState<any>(null);

    const callEndpoint = async () => {
        try {
            const response = await fetch(endpoint.url, {
                method: endpoint.method,
                headers: {
                    'Content-Type': 'application/json',
                },
            });
            const data = await response.json();
            setResponse(data);
        } catch (error) {
            console.error('Error calling endpoint:', error);
            setResponse('Error calling endpoint');
        }
    };

    const getButtonClassName = () => {
        return endpoint.method === 'GET' ? styles.buttonGET : styles.buttonPOST;
    };

    const getCodeExample = () => {
        return `
    import { fetchEndpoint } from 'codefly';
    const data = await fetchEndpoint({
    service: 'server',
    path: '/version',
    method: 'GET'
});

    return (
        <div className={styles.container}>
            <div className={styles.header}>
                <div className={styles.nameSection}>
                    <h4 className={styles.name}>{endpoint.name}</h4>
                    <div className={styles.methodRoute}>{endpoint.method}: {endpoint.route}</div>
                    <div className={styles.url}>{endpoint.url}</div>
                </div>
                <button 
                    onClick={callEndpoint}
                    className={getButtonClassName()}
                >
                    {endpoint.method}
                </button>
            </div>
            <pre className={styles.codeBlock}>
                <code>{getCodeExample()}</code>
            </pre>
            {response && (
                <div className={styles.responseSection}>
                    <h5 className={styles.responseTitle}>Response:</h5>
                    <pre className={styles.responseContent}>
                        {JSON.stringify(response, null, 2)}
                    </pre>
                </div>
            )}
        </div>
    );
} 
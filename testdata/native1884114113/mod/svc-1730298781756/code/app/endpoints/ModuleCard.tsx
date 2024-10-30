'use client';

import React, { useState } from 'react';
import { ModuleEndpoints } from '@/lib/codefly-sdk';
import ServiceGroup from './ServiceGroup';
import { ChevronDownIcon, ChevronRightIcon } from '@heroicons/react/24/solid';

const styles = {
    container: "bg-white dark:bg-gray-800 rounded-lg shadow-md p-6",
    moduleTitle: "text-xl font-semibold mb-4 text-gray-900 dark:text-gray-100 flex items-center cursor-pointer",
    moduleLabel: "bg-purple-100 text-purple-800 text-xs px-2 py-1 rounded mr-2",
    serviceList: "space-y-6",
    chevron: "w-5 h-5 mr-2",
}

interface ModuleCardProps {
    module: ModuleEndpoints;
}

export default function ModuleCard({ module }: ModuleCardProps) {
    const [isExpanded, setIsExpanded] = useState(true);

    return (
        <div className={styles.container}>
            <h2 
                className={styles.moduleTitle}
                onClick={() => setIsExpanded(!isExpanded)}
            >
                {isExpanded ? (
                    <ChevronDownIcon className={styles.chevron} />
                ) : (
                    <ChevronRightIcon className={styles.chevron} />
                )}
                <span className={styles.moduleLabel}>Module</span>
                {module.name}
            </h2>
            {isExpanded && (
                <div className={styles.serviceList}>
                    {module.services.map((serviceData) => (
                        <ServiceGroup 
                            key={`${module.name}-${serviceData.service}`}
                            serviceData={serviceData}
                            moduleName={module.name}
                        />
                    ))}
                </div>
            )}
        </div>
    );
} 
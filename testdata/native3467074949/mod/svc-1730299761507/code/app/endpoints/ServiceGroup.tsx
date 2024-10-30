'use client';

import React, { useState } from 'react';
import RouteItem from './RouteItem';
import { ChevronDownIcon, ChevronRightIcon } from '@heroicons/react/24/solid';

const styles = {
    container: "border-t border-gray-200 dark:border-gray-700 pt-4",
    serviceTitle: "text-lg font-medium mb-3 text-gray-800 dark:text-gray-200 flex items-center cursor-pointer",
    serviceLabel: "bg-blue-100 text-blue-800 text-xs px-2 py-1 rounded mr-2",
    routeList: "space-y-4",
    chevron: "w-5 h-5 mr-2",
}

interface ServiceGroupProps {
    serviceData: {
        service: string;
        address: string;
        routes: Array<{
            path: string;
            method: string;
            visibility: string;
        }>;
    };
    moduleName: string;
}

export default function ServiceGroup({ serviceData, moduleName }: ServiceGroupProps) {
    const [isExpanded, setIsExpanded] = useState(true);

    return (
        <div className={styles.container}>
            <h3 
                className={styles.serviceTitle}
                onClick={() => setIsExpanded(!isExpanded)}
            >
                {isExpanded ? (
                    <ChevronDownIcon className={styles.chevron} />
                ) : (
                    <ChevronRightIcon className={styles.chevron} />
                )}
                <span className={styles.serviceLabel}>Service</span>
                {serviceData.service}
            </h3>
            {isExpanded && (
                <div className={styles.routeList}>
                    {serviceData.routes.map((route) => (
                        <RouteItem 
                            key={`${moduleName}-${serviceData.service}-${route.path}`}
                            route={route}
                            serviceAddress={serviceData.address}
                            serviceName={serviceData.service}
                            moduleName={moduleName}
                        />
                    ))}
                </div>
            )}
        </div>
    );
} 
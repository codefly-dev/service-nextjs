'use client';

import React from 'react';
import { ModuleEndpoints } from '@/lib/codefly-sdk';
import ModuleCard from './ModuleCard';

const styles = {
    container: "container mx-auto p-4",
    moduleList: "space-y-8",
}

interface EndpointListProps {
    moduleEndpoints: ModuleEndpoints[];
}

export default function EndpointList({ moduleEndpoints }: EndpointListProps) {
    return (
        <div className={styles.container}>
            <div className={styles.moduleList}>
                {moduleEndpoints.map((module) => (
                    <ModuleCard key={module.name} module={module} />
                ))}
            </div>
        </div>
    );
}

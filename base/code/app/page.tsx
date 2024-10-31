import { Version } from '@/app/components/Version';
import { getCurrentService, getCurrentServiceVersion, endpoint } from 'codefly';
import Link from 'next/link';

const styles = {
    container: "min-h-screen bg-gradient-to-b from-gray-50 to-gray-100 dark:from-gray-800 dark:to-gray-900",
    content: "max-w-2xl mx-auto pt-20 p-8",
    titleSection: "text-center mb-12",
    mainTitle: "text-4xl font-bold text-gray-900 dark:text-white mb-2",
    subtitle: "text-lg text-gray-700 dark:text-gray-300",
    versionContainer: "bg-white dark:bg-gray-800 shadow-lg rounded-2xl p-8",
    endpointsSection: "text-center my-12",
    endpointsDescription: "text-gray-600 dark:text-gray-300 mb-4",
    link: "inline-flex items-center px-6 py-3 bg-blue-500 text-white rounded-lg hover:bg-blue-600 transition-colors",
}

export default function HomePage() {
    const currentService = getCurrentService();
    const serviceVersion = getCurrentServiceVersion();

    return (
        <div className={styles.container}>
            <div className={styles.content}>
                <div className={styles.titleSection}>
                    <h1 className={styles.mainTitle}>
                        Welcome to {currentService}!
                    </h1>
                    <p className={styles.subtitle}>
                        Version {serviceVersion}
                    </p>
                </div>

                <div className={styles.endpointsSection}>
                    <p className={styles.endpointsDescription}>
                        Explore and test your API endpoints with our interactive documentation.
                        Perfect for development and debugging.
                    </p>
                    <Link href="/endpoints" className={styles.link}>
                        View API Endpoints →
                    </Link>
                </div>
            </div>
        </div>
    );
}

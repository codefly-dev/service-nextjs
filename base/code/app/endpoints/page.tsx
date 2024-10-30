import { getEndpointsByModule } from 'codefly';
import EndpointList from './EndpointList';

export default function EndpointsPage() {
    const moduleEndpoints = getEndpointsByModule();

    return (
        <div className="container mx-auto p-4">
            <h1 className="text-2xl font-bold mb-4">Endpoints</h1>
            <EndpointList moduleEndpoints={moduleEndpoints} />
        </div>
    );
}

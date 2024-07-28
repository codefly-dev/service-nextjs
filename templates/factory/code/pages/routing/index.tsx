import Layout from "../../components/layout";

import { getEndpoints, getEndpointsByModule, ServiceEndpoint, ModuleEndpoints} from "codefly";

import Endpoint from "./endpoint";
import { ResponseDataProvider } from "../../providers/response.provider";
import DataView from "./data-view";

export const callApi = async (url: string | URL | Request) => {
  try {
    const response = await fetch(url);

    if (!response.ok) {
      console.error('Network response was not ok');
      return { success: false, error: 'Network response was not ok' };
    }
    const data = await response.json();
    return { success: true, data };
  } catch (error) {
    console.error('Error calling the API', error);
    return { success: false, error: error instanceof Error ? error.message : 'An unknown error occurred' };
  }
};

const Routing = ({ serviceEndpoints }) => {
  // Loop over modules
  const moduleRoutes: { [key: string]: ModuleEndpoints } = {};
  serviceEndpoints.forEach((endpoint: ServiceEndpoint) => {
    if (!moduleRoutes[endpoint.module]) {
      moduleRoutes[endpoint.module] = { name: endpoint.module, services: [] };
    }

    moduleRoutes[endpoint.module].services.push(endpoint);
  });
  // Convert to sorted array
  const moduleRoutesArray = Object.values(moduleRoutes).sort((a, b) => a.name.localeCompare(b.name));

  return (
    <Layout>
      <ResponseDataProvider>
        <div className="flex flex-col h-screen">
          <div className="flex flex-1">
            <div className="w-1/4 p-0 pt-0 pl-0 border-r dark:border-gray-800 ">
              <h2 className="pl-6 mb-8">Modules</h2>
              {moduleRoutesArray.map((module) => (
                  <ModuleRouting key={module.name} moduleEndpoints={module} />
              ))}
            </div>

            <DataView />
          </div>
        </div>
      </ResponseDataProvider>
    </Layout>
  );
};

const ModuleRouting = ({ moduleEndpoints }: { moduleEndpoints: ModuleEndpoints }) => {
  return (
      <div>
        <h3 className="pl-6 mb-4">{moduleEndpoints.name}</h3>
        {moduleEndpoints.services.map((service) => (
            <Endpoint key={service.service} endpoint={service} />
        ))}
      </div>
  );
};



function customReplacer(key: string, value: any) {
  if (key === 'routes' && Array.isArray(value)) {
    return value.map(route => ({
      path: route.path,
      method: route.method,
      visibility: route.visibility
    }));
  }
  return value;
}
export async function getServerSideProps() {

  const serviceEndpoints = getEndpoints();
  // getEndpointsByModule() doesn't work

  return { props: { serviceEndpoints } };
}

export default Routing;

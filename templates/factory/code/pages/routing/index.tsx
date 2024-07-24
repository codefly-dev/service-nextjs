import Layout from "../../components/layout";

import { getEndpoints, getEndpointsByModule} from "codefly";

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
  console.log("serviceEndpoints", serviceEndpoints)
  return (
    <Layout>
      <ResponseDataProvider>
        <div className="flex flex-col h-screen">
          <div className="flex flex-1">
            <div className="w-1/4 p-0 pt-0 pl-0 border-r dark:border-gray-800 ">
              <h2 className="pl-6 mb-8">Services</h2>

              <div className="overflow-y-auto" style={{ maxHeight: 'calc(100vh - 140px)' }}>
                {serviceEndpoints.map((e) => (<Endpoint key={e.service} endpoint={{ ...e }} />))}
              </div>
            </div>

            <DataView />
          </div>
        </div>
      </ResponseDataProvider>
    </Layout>
  );
};

export async function getServerSideProps() {

  const serviceEndpoints = getEndpoints();
  console.log("serviceEndpoints", serviceEndpoints)
  // getEndpointsByModule() doesn't work

  return { props: { serviceEndpoints } };
}

export default Routing;

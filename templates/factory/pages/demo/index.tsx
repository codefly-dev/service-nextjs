import Layout from "../../components/layout";


import { getEndpoints } from "codefly"
import Endpoint from "./endpoint";
import { ResponseDataProvider } from "./response.provider";
import DataView from "./data-view";

export const callApi = async (url) => {
  try {
    var response;
    response = await fetch(url);

    if (!response.ok) {
      throw new Error('Network response was not ok');
    }
    return response.json();
  } catch (error) {
    console.error('Error calling the API', error);
    throw error;
  }
};

const Demo = ({ serviceEndpoints }) => {
  return (
    <Layout>
      <ResponseDataProvider>
        <div className="flex flex-col h-screen">
          <div className="flex flex-1">
            <div className="w-1/4 p-0 pt-0 pl-0 border-r dark:border-gray-800 ">
              <h3 className="pl-6 mb-8">Services</h3>

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

  const serviceEndpoints = getEndpoints()

  return { props: { serviceEndpoints } };
}

export default Demo;

import { CopyBlock, dracula } from 'react-code-blocks';

import { JSONView } from "../../components/json-view";

import { useResponseData } from "./response.provider";
import DataViewInitital from "./components/data-view-initial";


const DataView = () => {
    const { response, endpoint, loading, route } = useResponseData();

    if (!response && !loading) {
        return <DataViewInitital />
    }

    return (
        <div className="w-3/4 pl-8 pt-0">
            <h2 className="pb-4">Response</h2>


            <p className="mt-2">
                In the code, we fetch the data with the SDK
            </p>

            <div className='max-w-[600px] mb-10 mr-6 border p-1 rounded-md'>
                <CopyBlock
                    text={`\nconst { routing } = useCodeflyContext();\nconst url = routing("${route.method}", "${endpoint.serviceName}", "${route.path}")\n\n`}
                    language={"javascript"}
                    showLineNumbers={false}
                    theme={dracula}
                />

            </div>

            <div className='max-w-[600px] mb-10 mr-6'>
                <h5 className="mb-1 ">Data loaded</h5>
                <JSONView>
                    {loading ? "Loading..." : response
                        ? JSON.stringify(response, null, 2)
                        : "Select an endpoint to test the response"}
                </JSONView>
            </div>

        </div>
    );
};


export default DataView;

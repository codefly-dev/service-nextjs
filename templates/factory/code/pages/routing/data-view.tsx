import { CopyBlock, dracula } from 'react-code-blocks';

import { JSONView } from "../../components/json-view";

import { useResponseData } from "../../providers/response.provider";
import DataViewInitial from "./components/data-view-initial";
import DataInput from './components/data-input';


const DataView = () => {
    const { response, endpoint, loading, route } = useResponseData();

    const method = route?.method;


    if (!response && !loading && !route) {
        return <DataViewInitial />
    }



    return (
        <div className="w-3/4 pl-8 pt-0">

            {method !== "GET" && <DataInput />}
            {response && <>
                <h2 className="pb-4">Response</h2>


                <p className="mt-2">
                    In the code, we fetch the data with the SDK
                </p>

                <div className='max-w-[900px] mb-10 mr-6 border p-1 rounded-md'>
                    <CopyBlock
                        text={`\nconst { routing } = useCodeflyContext();\nconst url = routing("${route.method}", "${endpoint.module}", "${endpoint.service}", "${route.path}")\n\n`}
                        language={"javascript"}
                        showLineNumbers={false}
                        theme={dracula}
                    />

                </div>

                <div className='max-w-[600px] mb-10 mr-6 '>
                    <h2 className="mb-1">Data</h2>
                    <div className='max-h-[250px] overflow-y-auto border rounded-md'>
                        <JSONView>
                            {loading ? "Loading..." : response
                                ? JSON.stringify(response, null, 2)
                                : ""}
                        </JSONView>
                    </div>
                </div>
            </>}

        </div>
    );
};


export default DataView;

import { useState } from 'react';
import { useResponseData } from '../../../providers/response.provider';
import { useCodeflyContext } from '../../../providers/codefly.provider';


const DataInput = () => {

    const [jsonInput, setJsonInput] = useState('');
    const [jsonData, setJsonData] = useState(null);
    const [error, setError] = useState(null);

    const { routing } = useCodeflyContext();
    const { setResponse, endpoint, setEndpoint, setLoading, route } = useResponseData();

    const handleFetch = async (route, data) => {
        const { method, path } = route;

        const url = routing(method, endpoint.module, endpoint.service, path)

        try {
            if (!url) {
                return;
            }

            setLoading(true);
            const response = await fetch( url, {
                method: method,
                headers: {
                    'Content-Type': 'application/json',
                },
                body: method !== "GET" ? JSON.stringify(data) : null,
            });
            const responseData = await response.json();
            setResponse(responseData);
            setLoading(false);

        } catch (error) {
            console.error('Error posting data:', error);
        }
    };

    const handleInputChange = (event) => {
        setJsonInput(event.target.value);
    };

    const handleSubmit = () => {
        setResponse(null);
        try {
            const parsedData = JSON.parse(jsonInput);
            setJsonData(parsedData);
            setError(null);
            handleFetch(route, parsedData)
        } catch (err) {
            setError('Invalid JSON format');
        }
    };

    return (
        <div className="w-3/4 pl-0 pt-0">
            <h2 className="pb-4">Data input</h2>

            <div>
                <textarea
                    value={jsonInput}
                    onChange={handleInputChange}
                    placeholder="Enter JSON data"
                    rows={10}
                    cols={50}
                />
            </div>

            {error && <div className="text-red-500">{error}</div>}

            <button
                className="bg-blue-500 hover:bg-blue-700 text-white font-bold py-2 px-4 mt-6 mb-6 rounded"
                type="button"
                onClick={handleSubmit}
            >
                Send
            </button>
        </div>
    );
};


export default DataInput;

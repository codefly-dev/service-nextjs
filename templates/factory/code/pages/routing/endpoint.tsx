import { useState } from "react";

import { DoubleArrowRightIcon } from "@radix-ui/react-icons";

import { useCodeflyContext } from "../../providers/codefly.provider";
import { useResponseData } from "../../providers/response.provider";

const getColorForMethod = (method: string) => {
    const colors = {
        'GET': 'green',
        'POST': 'yellow',
        'PATCH': 'emerald',
        'PUT' : 'emerald',
        'DELETE' : 'red'
    };

    // TODO : do this later
    return colors[method] ? `bg-${colors[method]}-500` : 'bg-green-500';
}

const Endpoint = ({ endpoint }) => {
    console.log("HERE IN ENPOINT", endpoint)
    const { routing } = useCodeflyContext();
    const { setResponse, setEndpoint, setLoading, setRoute } = useResponseData();

    const handleFetch = async (route, data) => {
        const { method, path } = route;

        const url = routing(method, endpoint.module, endpoint.service, path)
        console.log("URL", url)
        setLoading(true);
        try {
            const response = await fetch(url, {
                method: method,
                headers: {
                    'Content-Type': 'application/json',
                },
                body: method !== "GET" ? JSON.stringify(data) : null,
            });

            if (!response.ok) {
                console.error('Network response was not ok');
                setResponse({ success: false, error: 'Network response was not ok', statusCode: response.status });
                setLoading(false);
                return;
            }

            const contentType = response.headers.get('Content-Type');
            let responseData;
            if (contentType && contentType.includes('application/json')) {
                responseData = await response.json();
            } else {
                responseData = await response.text();
            }

            setResponse({ success: true, data: responseData });
        } catch (error) {
            console.error('Error fetching data:', error);
            setResponse({ success: false, error: error instanceof Error ? error.message : 'An unknown error occurred' });
        } finally {
            setLoading(false);
        }
    };

    const [isContentVisible, setIsContentVisible] = useState<boolean>(false);

    const handleRouteClick = (route) => {
        setResponse(null)
        if (route.method === 'GET') {

            handleFetch(route, null)
        }
        setRoute(route);

    }

    const handleEndpointClick = () => {
        setIsContentVisible(!isContentVisible);
        setEndpoint(endpoint)
    }

    return (
        <div className="bg-gray-200 dark:bg-gray-800 p-0 mb-4 cursor-pointer hover:bg-gray-300 border m-8 mt-0 border-gray-400 rounded-md">
            <ul className="list-none p-0">
                <li className="font-bold p-4 pl-8 flex" onClick={handleEndpointClick}>{endpoint.service} <DoubleArrowRightIcon className="w-4 h-4 text-neutral-300 ml-2 mt-1 " /></li>
                <div className={`transition-opacity transition-height overflow-hidden ${isContentVisible ? 'opacity-100 h-auto' : 'opacity-0 h-0'}`}>
                    {
                        endpoint.routes.map(r => (
                            <li onClick={() => handleRouteClick(r)} key={r.path} className="bg-gray-100 dark:bg-gray-700 cursor-pointer hover:bg-gray-200 dark:hover:bg-gray-600 p-2 pl-10 border-b border-gray-400">
                                <span className={` ${getColorForMethod(r.method)} p-1 pl-2 pr-2 rounded-md mr-1`}>{r.method}</span>
                                <span className="bg-gray-500 p-1 pl-2 pr-2 rounded-md mr-1 italic text-white">{r.path}</span></li>
                        ))
                    }
                </div>
            </ul>
        </div>
    );
};


export default Endpoint;

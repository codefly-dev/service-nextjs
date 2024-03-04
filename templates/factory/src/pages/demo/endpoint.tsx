import { useState } from "react";

import { DoubleArrowRightIcon } from "@radix-ui/react-icons";

import { useCodeflyContext } from "../providers/codefly.provider";
import { useResponseData } from "./response.provider";


const Endpoint = ({ endpoint }) => {
    const { routing } = useCodeflyContext();
    const { setResponse, setEndpoint, setLoading, setRoute } = useResponseData();

    const hanldeFetch = async (route, data) => {
        const { method, path } = route;

        const url = routing(method, endpoint.service, path)

        try {
            if (!url) {
                return;
            }

            setLoading(true);
            const response = await fetch("http://" + url, {
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

    const [isContentVisible, setIsContentVisible] = useState<boolean>(false);

    const handleRouteClick = (route) => {
        if (route.method === 'GET') {

            hanldeFetch(route, null)
            setRoute(route);
        }
    }

    const handleEndpointClick = () => {
        setIsContentVisible(!isContentVisible);
        setEndpoint(endpoint)
    }

    return (
        <div className="bg-gray-200 dark:bg-gray-800 p-0 mb-4 cursor-pointer hover:bg-gray-300 border m-8 mt-0 border-gray-400 rounded-md">
            <ul className="list-none p-0">
                <li className="font-bold p-4 pl-8 flex" onClick={handleEndpointClick}>{endpoint.serviceName} <DoubleArrowRightIcon className="w-4 h-4 text-neutral-300 ml-2 mt-1 " /></li>
                <div className={`transition-opacity transition-height overflow-hidden ${isContentVisible ? 'opacity-100 h-auto' : 'opacity-0 h-0'}`}>
                    {
                        endpoint.routes.map(r => (
                            <li onClick={() => handleRouteClick(r)} key={r.path} className="bg-gray-100 dark:bg-gray-700 cursor-pointer hover:bg-gray-200 dark:hover:bg-gray-600 p-2 pl-10 border-b border-gray-400">
                                <span className="bg-green-500 p-1 pl-2 pr-2 rounded-md mr-1">{r.method}</span>
                                <span className="bg-gray-500 p-1 pl-2 pr-2 rounded-md mr-1 italic text-white">{r.path}</span></li>
                        ))
                    }
                </div>
            </ul>
        </div>
    );
};


export default Endpoint;

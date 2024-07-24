// context.ts
import React, { createContext, useState, useContext } from 'react';
import { routing as _routing, getEndpoints } from "codefly"


interface ContextProps {
    data: { endpoints : any[] }; // Define the type of your data
    setData: React.Dispatch<React.SetStateAction<any>>;
}

const CodeflyContext = createContext<ContextProps | undefined>(undefined);

const CodeflyContextProvider = ({ children, endpoints }: {
    children: React.ReactNode;
    endpoints: any[];
}) => {
    const [data, setData] = useState<any>({endpoints}); // Initialize with your initial data

    return (
        <CodeflyContext.Provider value={{ data, setData }}>
            {children}
        </CodeflyContext.Provider>
    );
};

// hook to consume the context
const useCodeflyContext = () => {
    const context = useContext(CodeflyContext);

    if (context === undefined) {
        throw new Error('useCodeflyContext must be used within an CodeflyContextProvider');
    }

    const routing = (method: string, endpoint: string, path: string) => {
        return _routing[method](endpoint, path, [...context.data.endpoints])
    }
    
    return { routing };
};

export { CodeflyContextProvider, useCodeflyContext };

export default CodeflyContextProvider;

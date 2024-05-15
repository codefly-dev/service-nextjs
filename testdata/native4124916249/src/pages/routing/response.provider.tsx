// context.ts
import React, { createContext, useState, useContext, FC } from 'react';

interface ContextProps {
  response: any; 
  setResponse: React.Dispatch<React.SetStateAction<any>>;
  endpoint: any; 
  setEndpoint: React.Dispatch<React.SetStateAction<any>>;
  route: any; 
  setRoute: React.Dispatch<React.SetStateAction<any>>;
  loading: any; 
  setLoading: React.Dispatch<React.SetStateAction<any>>;
}

type Props = {
  children: string | React.JSX.Element | React.JSX.Element[];
};

const ResponseDataContext = createContext<ContextProps | undefined>(undefined);

const ResponseDataProvider = ({ children }: Props) => {
  const [response, setResponse] = useState<any>();
  const [endpoint, setEndpoint] = useState<any>();
  const [route, setRoute] = useState<any>();
  const [loading, setLoading] = useState<any>();

  return (
    <ResponseDataContext.Provider value={{ response, setResponse, endpoint, setEndpoint, loading, setLoading, route, setRoute }}>
      {children}
    </ResponseDataContext.Provider>
  );
};

// Create a custom hook to consume the context
const useResponseData = () => {
  const context = useContext(ResponseDataContext);
  if (context === undefined) {
    throw new Error('useResponseData must be used within an ResponseDataProvider');
  }
  return context;
};

export { ResponseDataProvider, useResponseData };

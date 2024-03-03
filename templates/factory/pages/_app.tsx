import "../styles/globals.css";
import { CodeflyContextProvider } from "./providers/codefly.provider";

import { routing, getEndpoints } from "codefly"
import  { AppContext } from 'next/app'

type AppOwnProps = { Component?: any; pageProps?: any; endpoints: any; }

export default function App({ Component, pageProps, endpoints }) {  
  return (
    // This provides codefly info to the app to use in the client side rendering
    <CodeflyContextProvider endpoints={endpoints}>
      <Component {...pageProps} />
    </CodeflyContextProvider>
  );
}

App.getInitialProps = async (
  context: AppContext
): Promise<AppOwnProps> => {
 
  const endpoints = getEndpoints();

  return { endpoints }
}

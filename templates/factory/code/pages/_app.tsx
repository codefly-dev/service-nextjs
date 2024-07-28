import "../styles/globals.css";
import {CodeflyContextProvider} from "../providers/codefly.provider";

import {getEndpoints} from "codefly";

import {AppContext} from 'next/app';
import AuthWrapper from "./authwrapper";


type AppOwnProps = { Component?: any; pageProps?: any; endpoints: any; }

export default function App({Component, pageProps, endpoints}) {
    return (
        <AuthWrapper>
            // This provides codefly info to the app to use in the client side rendering
            <CodeflyContextProvider endpoints={endpoints}>
                <Component {...pageProps} />
            </CodeflyContextProvider>
        </AuthWrapper>
    );
}

App.getInitialProps = async (
    context: AppContext
): Promise<AppOwnProps> => {

    const endpoints = getEndpoints();

    return {endpoints}
}

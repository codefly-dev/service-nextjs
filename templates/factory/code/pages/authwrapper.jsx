import React from 'react';
import { Auth0Provider } from '@auth0/auth0-react';
import App from './_app';

const domain = process.env.REACT_APP_AUTH0_DOMAIN;
const clientId = process.env.REACT_APP_AUTH0_CLIENT_ID;
const audience = process.env.REACT_APP_AUTH0_API_AUDIENCE;

const authType = process.env.REACT_APP_AUTH_TYPE;

const AuthWrapper = ({ children }) => {
    switch (authType) {
        case 'auth0':
            return (
                <Auth0Provider
                    domain={domain}
                    clientId={clientId}
                    redirectUri={window.location.origin}
                    audience={audience}
                >
                    {children}
                </Auth0Provider>
            );
        // Add more cases for other auth providers
        case 'none':
        default:
            return <>{children}</>;
    }
};

export default AuthWrapper;
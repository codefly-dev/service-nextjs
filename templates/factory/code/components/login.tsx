import React from 'react';
import { useAuth0 } from '@auth0/auth0-react';

const LoginButton = () => {
    const { loginWithRedirect } = useAuth0();
    return <button onClick={() => loginWithRedirect()}>Log In</button>;
};

const ConditionalLoginButton = () => {
    const authType = process.env.REACT_APP_AUTH_TYPE;
    if (authType !== 'auth0') {
        return null;
    }
    return <LoginButton />;
};

export default ConditionalLoginButton;
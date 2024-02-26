import { withPageAuthRequired } from "@auth0/nextjs-auth0/client";
import Layout from "../components/layout";
import { UserInfoCard } from "../components/user-info-card";
import { User } from "../interfaces";

type ProfileCardProps = {
  user: User;
};

const ProfileCard = ({ user }: ProfileCardProps) => {
  return (
    <>
      <h2>Profile</h2>
      <div>
        <UserInfoCard user={user} />
      </div>
    </>
  );
};

const Profile = ({ user, isLoading }) => {
  return (
    <Layout user={user} loading={isLoading}>
      {isLoading ? <>Loading...</> : <ProfileCard user={user} />}
    </Layout>
  );
};

// Protected route, checking user authentication client-side.(CSR)
export default withPageAuthRequired(Profile);

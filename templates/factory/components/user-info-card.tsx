import { UserProfile } from "@auth0/nextjs-auth0/client";

export const UserInfoCard = ({ user }: { user: UserProfile }) => {
  return (
    <div className="globals__card flex flex-col items-center gap-2 px-3 py-10">
      <img
        src={user.picture}
        alt="user picture"
        className="rounded-full w-16 h-16"
      />
      <div className="flex flex-col justify-center">
        <div className="flex items-center gap-1">
          <span className="text-neutral-400 font-light text-sm">Nickname</span>
          <span className="font-medium">{user.nickname}</span>
        </div>

        <div className="flex gap-1">
          <span className="text-neutral-400 font-light text-sm">Name</span>
          <span className="font-medium">{user.name}</span>
        </div>
      </div>
    </div>
  );
};
